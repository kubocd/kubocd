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

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAHelmReleaseThatTheReleaseDoesNotControlIsLeftAlone(t *testing.T) {
	const theirValues = `{"theirs":true}`
	controller := true
	owners := map[string][]metav1.OwnerReference{
		"of another Release": {{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release", Name: "other", UID: "other",
			Controller: &controller}},
		"of an earlier Release of the same name": {{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release", Name: "demo",
			UID: "former", Controller: &controller}},
		"without owner": nil,
	}
	for name, owner := range owners {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, module enabled %t", name, enabled), func(t *testing.T) {
				helmRelease := readyHelmRelease()
				helmRelease.SetOwnerReferences(owner)
				helmRelease.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(theirValues)}
				moduleFields := ""
				if !enabled {
					moduleFields = `    enabled: "false"`
				}
				testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
					withObjects(t, helmRelease))
				got := testbed.reconcile(t)
				if enabled {
					want := "HelmRelease 'demo-main' exists and is not controlled by this Release"
					if got.err == nil || got.status != want {
						t.Fatalf("got error %v and status %q, want an error and the status %q", got.err, got.status, want)
					}
				} else if got.err != nil {
					t.Fatalf("got error %v, want none for a disabled module", got.err)
				}
				left := &fluxv2.HelmRelease{}
				if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, left); err != nil {
					t.Fatalf("the HelmRelease is gone: %v", err)
				}
				if values := string(left.Spec.Values.Raw); values != theirValues {
					t.Fatalf("got the values %s of the HelmRelease, want %s", values, theirValues)
				}
			})
		}
	}
}

// getDemoMain reads the HelmRelease of the module main.
func getDemoMain(t *testing.T, testbed *releaseTestbed) *fluxv2.HelmRelease {
	t.Helper()
	helmRelease := &fluxv2.HelmRelease{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, helmRelease); err != nil {
		t.Fatalf("the HelmRelease is gone: %v", err)
	}
	return helmRelease
}

func TestAHelmReleaseReplacedOnceReadIsLeftAlone(t *testing.T) {
	const theirValues = `{"theirs":true}`
	controller := true
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("module enabled %t", enabled), func(t *testing.T) {
			mine := readyHelmRelease()
			mine.UID = "mine"
			replacement := readyHelmRelease()
			replacement.UID = "theirs"
			replacement.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release",
				Name: "other", UID: "other", Controller: &controller}})
			replacement.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(theirValues)}
			moduleFields := ""
			if !enabled {
				moduleFields = `    enabled: "false"`
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
				withObjects(t, mine), changingAfterRead(t, client.ObjectKeyFromObject(mine), replacedBy(t, replacement)))
			if got := testbed.reconcile(t); got.err == nil {
				t.Fatalf("got no error and the status %q, want the write refused", got.status)
			}
			left := getDemoMain(t, testbed)
			if left.UID != "theirs" || string(left.Spec.Values.Raw) != theirValues {
				t.Fatalf("got the HelmRelease %s with the values %s, want the replacement unchanged", left.UID, left.Spec.Values.Raw)
			}
		})
	}
}

// A control: a HelmRelease written again between the read and the write, with
// the same UID, does not make the write fail.
func TestARewriteOfTheHelmReleaseOnceReadDoesNotFailTheWrite(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("module enabled %t", enabled), func(t *testing.T) {
			mine := readyHelmRelease()
			mine.UID = "mine"
			moduleFields := ""
			if !enabled {
				moduleFields = `    enabled: "false"`
			}
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", moduleFields), "", afterRenderContext,
				withObjects(t, mine), changingAfterRead(t, client.ObjectKeyFromObject(mine), rewritten(t)))
			if got := testbed.reconcile(t); got.err != nil {
				t.Fatalf("got the error %v, want the HelmRelease written", got.err)
			}
			left := &fluxv2.HelmRelease{}
			err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, left)
			if enabled && (err != nil || left.Spec.Chart == nil) {
				t.Fatalf("got the error %v and the HelmRelease %+v, want it patched", err, left.Spec)
			}
			if !enabled && err == nil {
				t.Fatalf("got the HelmRelease of a disabled module, want it deleted")
			}
		})
	}
}
