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
	"encoding/json"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// getDemo reads the Release demo.
func getDemo(t *testing.T, testbed *releaseTestbed) *kv1alpha1.Release {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	return release
}

// outputOfDemo returns the status of the output db of the Release demo.
func outputOfDemo(t *testing.T, testbed *releaseTestbed) kv1alpha1.ReleaseOutputConnection {
	t.Helper()
	return getDemo(t, testbed).Status.OutputConnectionByName["db"]
}

func TestTheStatusOfAReleaseNamesItsOutputConnectionFromItsCreation(t *testing.T) {
	cases := map[kv1alpha1.Kind]kv1alpha1.ReleaseOutputConnection{
		kv1alpha1.KindConnection:        {Kind: kv1alpha1.KindConnection, Name: "kcd-demo-db", Namespace: "apps"},
		kv1alpha1.KindClusterConnection: {Kind: kv1alpha1.KindClusterConnection, Name: "kcd-apps-demo-db"},
	}
	for kind, want := range cases {
		t.Run(string(kind), func(t *testing.T) {
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kind, ""), "")
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease()))
			for _, pass := range []string{"creation", "update"} {
				if got := testbed.reconcile(t); got.err != nil {
					t.Fatalf("%s: got error %v", pass, got.err)
				}
				if got := outputOfDemo(t, testbed); got != want {
					t.Fatalf("%s: got the output %+v, want %+v", pass, got, want)
				}
			}
		})
	}
}

// clusterConnectionDb is the ClusterConnection that the output db of the
// Release demo names, READY, with the parent Release given, if any.
func clusterConnectionDb(parent *kv1alpha1.ParentReleaseRef, values string) *kv1alpha1.ClusterConnection {
	return &kv1alpha1.ClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: "kcd-apps-demo-db"},
		Spec: kv1alpha1.ClusterConnectionSpec{Contract: databaseContract, Values: &apiextensionsv1.JSON{Raw: []byte(values)},
			ParentRelease: parent},
		Status: kv1alpha1.ClusterConnectionStatus{Phase: kv1alpha1.ConnectionPhaseReady},
	}
}

// getClusterConnectionDb reads the ClusterConnection of clusterConnectionDb.
func getClusterConnectionDb(t *testing.T, testbed *releaseTestbed) *kv1alpha1.ClusterConnection {
	t.Helper()
	clusterConnection := &kv1alpha1.ClusterConnection{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Name: "kcd-apps-demo-db"}, clusterConnection); err != nil {
		t.Fatal(err)
	}
	return clusterConnection
}

func TestAClusterConnectionOfAnotherReleaseIsLeftAlone(t *testing.T) {
	const theirs, refused = `{"host":"theirs"}`, "output[0]: its ClusterConnection belongs to another Release"
	demo := &kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}
	cases := map[string]struct {
		parent     *kv1alpha1.ParentReleaseRef
		wantStatus string
		wantValues string
		wantParent *kv1alpha1.ParentReleaseRef
	}{
		"of another Release in another namespace": {&kv1alpha1.ParentReleaseRef{Namespace: "other", Name: "other"}, refused, theirs,
			&kv1alpha1.ParentReleaseRef{Namespace: "other", Name: "other"}},
		"of a Release of the same name in another namespace": {&kv1alpha1.ParentReleaseRef{Namespace: "other", Name: "demo"}, refused,
			theirs, &kv1alpha1.ParentReleaseRef{Namespace: "other", Name: "demo"}},
		"of another Release in the same namespace": {&kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "other"}, refused, theirs,
			&kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "other"}},
		"of the Release":   {demo, "", `{"host":"pg"}`, demo},
		"without a parent": {nil, "", `{"host":"pg"}`, demo},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kv1alpha1.KindClusterConnection, ""), "")
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext,
				withObjects(t, readyHelmRelease(), clusterConnectionDb(c.parent, theirs)))
			got := testbed.reconcile(t)
			if got.status != c.wantStatus {
				t.Fatalf("got the status %q, want %q", got.status, c.wantStatus)
			}
			// Retried, as no watch tells the Release when the other one frees the name
			if phase := getDemo(t, testbed).Status.Phase; c.wantStatus == refused && (got.err == nil || phase != kv1alpha1.ReleasePhaseError) {
				t.Fatalf("got the error %v and the phase %q, want an error to retry and ERROR", got.err, phase)
			}
			left := getClusterConnectionDb(t, testbed)
			if values := string(left.Spec.Values.Raw); values != c.wantValues || *left.Spec.ParentRelease != *c.wantParent {
				t.Fatalf("got the values %s and the parent %+v, want %s and %+v", values, left.Spec.ParentRelease, c.wantValues,
					c.wantParent)
			}
		})
	}
}

// changingAfterRead applies change to the object of key once the controller
// read it. Its API server refuses, as a real one does, a patch that carries
// another UID than that of the object it holds, and a deletion whose
// precondition names another UID.
func changingAfterRead(t *testing.T, key client.ObjectKey, change func(ctx context.Context, c client.WithWatch, read client.Object)) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		changed := false
		heldUID := func(ctx context.Context, c client.WithWatch, obj client.Object) types.UID {
			current := obj.DeepCopyObject().(client.Object)
			if err := c.Get(ctx, key, current); err != nil {
				t.Fatal(err)
			}
			return current.GetUID()
		}
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, k, obj, opts...); err != nil || k != key || changed {
					return err
				}
				changed = true
				change(ctx, c, obj.DeepCopyObject().(client.Object))
				return nil
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if client.ObjectKeyFromObject(obj) == key {
					data, err := patch.Data(obj)
					if err != nil {
						return err
					}
					var meta struct {
						Metadata struct {
							UID types.UID `json:"uid"`
						} `json:"metadata"`
					}
					if err := json.Unmarshal(data, &meta); err != nil {
						return err
					}
					if uid := meta.Metadata.UID; uid != "" && uid != heldUID(ctx, c, obj) {
						gvk, err := apiutil.GVKForObject(obj, c.Scheme())
						if err != nil {
							return err
						}
						return apierrors.NewInvalid(gvk.GroupKind(), obj.GetName(),
							field.ErrorList{field.Invalid(field.NewPath("metadata", "uid"), uid, "field is immutable")})
					}
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if client.ObjectKeyFromObject(obj) == key {
					deleteOptions := client.DeleteOptions{}
					deleteOptions.ApplyOptions(opts)
					if p := deleteOptions.Preconditions; p != nil && p.UID != nil && *p.UID != heldUID(ctx, c, obj) {
						gvk, err := apiutil.GVKForObject(obj, c.Scheme())
						if err != nil {
							return err
						}
						return apierrors.NewConflict(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, obj.GetName(),
							errors.New("the UID in the precondition does not match the UID in record"))
					}
				}
				return c.Delete(ctx, obj, opts...)
			},
		})
	}
}

// replacedBy deletes the object read and creates replacement, under the same
// name.
func replacedBy(t *testing.T, replacement client.Object) func(ctx context.Context, c client.WithWatch, read client.Object) {
	return func(ctx context.Context, c client.WithWatch, read client.Object) {
		if err := c.Delete(ctx, read); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
	}
}

// rewritten writes the object read again, as its controller does with its
// status, which changes its resourceVersion and keeps its UID.
func rewritten(t *testing.T) func(ctx context.Context, c client.WithWatch, read client.Object) {
	return func(ctx context.Context, c client.WithWatch, read client.Object) {
		read.SetAnnotations(map[string]string{"test.kubocd.kubotal.io/written": "again"})
		if err := c.Update(ctx, read); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAClusterConnectionReplacedOnceReadIsLeftAlone(t *testing.T) {
	const theirs = `{"host":"theirs"}`
	mine := clusterConnectionDb(&kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}, `{"host":"old"}`)
	mine.UID = "mine"
	replacement := clusterConnectionDb(&kv1alpha1.ParentReleaseRef{Namespace: "other", Name: "other"}, theirs)
	replacement.UID = "theirs"
	manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kv1alpha1.KindClusterConnection, ""), "")
	key := client.ObjectKeyFromObject(mine)
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease(), mine),
		changingAfterRead(t, key, replacedBy(t, replacement)))
	if got := testbed.reconcile(t); got.err == nil {
		t.Fatalf("got no error and the status %q, want the patch refused", got.status)
	}
	left := getClusterConnectionDb(t, testbed)
	if values := string(left.Spec.Values.Raw); left.UID != "theirs" || values != theirs || left.Spec.ParentRelease.Name != "other" {
		t.Fatalf("got the ClusterConnection %s with the values %s and the parent %+v, want the replacement unchanged", left.UID,
			values, left.Spec.ParentRelease)
	}
}

// A control: an object written again between the read and the patch, with the
// same UID, does not make the patch fail.
func TestARewriteOfTheClusterConnectionOnceReadDoesNotFailThePatch(t *testing.T) {
	mine := clusterConnectionDb(&kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}, `{"host":"old"}`)
	mine.UID = "mine"
	manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kv1alpha1.KindClusterConnection, ""), "")
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, readyHelmRelease(), mine),
		changingAfterRead(t, client.ObjectKeyFromObject(mine), rewritten(t)))
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got the error %v, want the ClusterConnection patched", got.err)
	}
	if values := string(getClusterConnectionDb(t, testbed).Spec.Values.Raw); values != `{"host":"pg"}` {
		t.Fatalf("got the values %s, want those of the output", values)
	}
}
