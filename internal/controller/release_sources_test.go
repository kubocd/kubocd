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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"testing"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// withSourceReady sets the Ready condition of the Flux source of the Release
// demo, of the kind given.
func withSourceReady(t *testing.T, kind string, status metav1.ConditionStatus, message string) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		ctx := context.Background()
		key := types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(OciRepositoryNameFormat, "demo")}
		ready := []metav1.Condition{{Type: "Ready", Status: status, Reason: "Failed", Message: message, LastTransitionTime: metav1.Now()}}
		if kind == "OCIRepository" {
			source := &sourcev1.OCIRepository{}
			if err := r.Get(ctx, key, source); err != nil {
				t.Fatal(err)
			}
			source.Status.Conditions = ready
			if err := r.Update(ctx, source); err != nil {
				t.Fatal(err)
			}
			return
		}
		source := &sourcev1.HelmRepository{}
		if err := r.Get(ctx, key, source); err != nil {
			t.Fatal(err)
		}
		source.Status.Conditions = ready
		if err := r.Update(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAFluxSourceThatIsNotReadyGivesItsMessage(t *testing.T) {
	const (
		pullFailure  = "failed to pull artifact from 'oci://registry.example/demo': GET https://registry.example/v2/demo: UNAUTHORIZED"
		indexFailure = "failed to fetch Helm repository index: 404 Not Found"
	)
	cases := map[string]struct {
		kind    string
		status  metav1.ConditionStatus
		message string
		phase   kv1alpha1.ReleasePhase
		want    string
	}{
		"OCIRepository failed": {"OCIRepository", metav1.ConditionFalse, pullFailure,
			kv1alpha1.ReleasePhaseWaitOci, "OCIRepository 'kcd-demo' is not ready: " + pullFailure},
		"OCIRepository failed without a message": {"OCIRepository", metav1.ConditionFalse, "",
			kv1alpha1.ReleasePhaseWaitOci, "OCIRepository 'kcd-demo' is not ready"},
		"OCIRepository in progress": {"OCIRepository", metav1.ConditionUnknown, "reconciliation in progress",
			kv1alpha1.ReleasePhaseWaitOci, "Wait OCI repository"},
		"HelmRepository failed": {"HelmRepository", metav1.ConditionFalse, indexFailure,
			kv1alpha1.ReleasePhaseWaitHelmRepo, "HelmRepository 'kcd-demo' is not ready: " + indexFailure},
		"HelmRepository in progress": {"HelmRepository", metav1.ConditionUnknown, "reconciliation in progress",
			kv1alpha1.ReleasePhaseWaitHelmRepo, "Wait Helm Repository"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
				withSourceReady(t, c.kind, c.status, c.message))
			got := testbed.reconcile(t)
			release := &kv1alpha1.Release{}
			if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
				t.Fatal(err)
			}
			if got.err != nil || release.Status.Phase != c.phase || got.status != c.want {
				t.Fatalf("got error %v, phase %q and status %q, want phase %q and status %q", got.err, release.Status.Phase,
					got.status, c.phase, c.want)
			}
		})
	}
}
