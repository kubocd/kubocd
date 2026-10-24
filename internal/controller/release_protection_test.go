/*
Copyright 2026 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

//nolint:goconst // test inputs read better inline
package controller

import (
	"context"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var demoKey = types.NamespacedName{Namespace: "apps", Name: "demo"}

func demoRelease(t *testing.T, testbed *releaseTestbed) *kv1alpha1.Release {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), demoKey, release); err != nil {
		t.Fatal(err)
	}
	return release
}

// settle reconciles the Release demo until a reconciliation leaves it as it
// was.
func settle(t *testing.T, testbed *releaseTestbed) {
	t.Helper()
	for range 10 {
		before := demoRelease(t, testbed).ResourceVersion
		testbed.reconcile(t)
		if demoRelease(t, testbed).ResourceVersion == before {
			return
		}
	}
	t.Fatalf("the Release still changes after 10 reconciliations")
}

// clearPrintProtected empties status.printProtected of the Release demo, and
// keeps the rest of its status.
func clearPrintProtected(t *testing.T, testbed *releaseTestbed) {
	t.Helper()
	release := demoRelease(t, testbed)
	release.Status.PrintProtected = ""
	if err := testbed.client.Status().Update(context.Background(), release); err != nil {
		t.Fatal(err)
	}
}

// withMissingContext makes the Release demo use a Context that does not exist.
func withMissingContext(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		release := &kv1alpha1.Release{}
		if err := r.Get(context.Background(), demoKey, release); err != nil {
			t.Fatal(err)
		}
		release.Spec.Contexts = []kv1alpha1.NamespacedName{{Namespace: "apps", Name: "missing"}}
		if err := r.Update(context.Background(), release); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheProtectionOfAReleaseIsWrittenWhenItsPrintedFormChanges(t *testing.T) {
	unprotected := fmt.Sprintf(reconciledPackage, "replicaCount: 1")
	protected := strings.Replace(unprotected, "tag: 0.1.0\n", "tag: 0.1.0\nprotected: true\n", 1)
	cases := map[string]struct {
		manifest string
		options  []func(*ReleaseReconciler)
		phase    kv1alpha1.ReleasePhase
		want     string
	}{
		"package without protection":         {unprotected, nil, kv1alpha1.ReleasePhaseWaitHelmReleases, "-"},
		"protected package":                  {protected, nil, kv1alpha1.ReleasePhaseWaitHelmReleases, "X"},
		"error after the protection is read": {unprotected, []func(*ReleaseReconciler){withMissingContext(t)}, kv1alpha1.ReleasePhaseError, "-"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, c.manifest, "", "", c.options...)
			settle(t, testbed)
			if release := demoRelease(t, testbed); release.Status.Phase != c.phase || release.Status.PrintProtected != c.want {
				t.Fatalf("got the phase %q and printProtected %q once settled, want %q and %q", release.Status.Phase,
					release.Status.PrintProtected, c.phase, c.want)
			}
			clearPrintProtected(t, testbed)
			testbed.reconcile(t)
			if release := demoRelease(t, testbed); release.Status.Phase != c.phase || release.Status.PrintProtected != c.want {
				t.Fatalf("got the phase %q and printProtected %q, want %q and %q", release.Status.Phase,
					release.Status.PrintProtected, c.phase, c.want)
			}
		})
	}
}

func TestAProtectionWhoseStatusWriteFailedIsWrittenByTheNextReconciliation(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(reconciledPackage, "replicaCount: 1"), "", "")
	settle(t, testbed)
	clearPrintProtected(t, testbed)
	refused := 0
	testbed.reconciler.Client = interceptor.NewClient(testbed.client, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*kv1alpha1.Release); ok && refused == 0 {
				refused++
				return errors.New("the API server is not reachable")
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	testbed.reconcile(t)
	if got := demoRelease(t, testbed).Status.PrintProtected; refused != 1 || got != "" {
		t.Fatalf("got %d refused writes and printProtected %q, want the write refused and the marker still empty", refused, got)
	}
	testbed.reconcile(t)
	if got := demoRelease(t, testbed).Status.PrintProtected; got != "-" {
		t.Fatalf("got printProtected %q after the retry, want -", got)
	}
}
