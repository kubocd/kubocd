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
	"kubocd/internal/global"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// answeringDeletes answers the deletion of the ClusterConnection named with
// err, without deleting it.
func answeringDeletes(name string, err error) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*kv1alpha1.ClusterConnection); ok && obj.GetName() == name {
					return err
				}
				return c.Delete(ctx, obj, opts...)
			},
		})
	}
}

// clusterConnectionOf is an output ClusterConnection of the Release demo.
func clusterConnectionOf(name string) *kv1alpha1.ClusterConnection {
	return &kv1alpha1.ClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kv1alpha1.ClusterConnectionSpec{Contract: postgresContract,
			ParentRelease: &kv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "demo"}},
	}
}

func TestTheFinalizerOfAReleaseStaysUntilItsClusterConnectionsAreDeleted(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "clusterconnections"},
		"kcd-apps-demo-a", fmt.Errorf("denied by the policy %q", canaryPassword))
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "clusterconnections"},
		"kcd-apps-demo-a")
	cases := map[string]struct {
		options   func(t *testing.T) []func(*ReleaseReconciler)
		wantErr   string
		finalizer bool
		deleted   []string
		left      []string
	}{
		"list refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a")),
				refusingOwnerLists(ReleaseIndexOnOutputClusterConnection, "clusterconnections")}
		}, "could not list the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true, nil,
			[]string{"kcd-apps-demo-a"}},
		"first deletion refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a"), clusterConnectionOf("kcd-apps-demo-b")),
				answeringDeletes("kcd-apps-demo-a", forbidden)}
		}, "could not delete the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true,
			[]string{"kcd-apps-demo-b"}, []string{"kcd-apps-demo-a"}},
		"second deletion refused": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a"), clusterConnectionOf("kcd-apps-demo-b")),
				answeringDeletes("kcd-apps-demo-b", forbidden)}
		}, "could not delete the ClusterConnections of the Release: the API server answered Forbidden, " + objectWithheld, true,
			[]string{"kcd-apps-demo-a"}, []string{"kcd-apps-demo-b"}},
		// A control: a ClusterConnection deleted meanwhile is not a failure.
		"ClusterConnection already gone": {func(t *testing.T) []func(*ReleaseReconciler) {
			return []func(*ReleaseReconciler){withObjects(t, clusterConnectionOf("kcd-apps-demo-a")), answeringDeletes("kcd-apps-demo-a", notFound)}
		}, "", false, nil, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
				append(c.options(t), deletingTheRelease(t))...)
			got := testbed.reconcile(t)
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the answer:\n%s", sink, text)
				}
			}
			if c.wantErr == "" && got.err != nil || c.wantErr != "" && (got.err == nil || got.err.Error() != c.wantErr) {
				t.Fatalf("got the error %v, want %q", got.err, c.wantErr)
			}
			release := &kv1alpha1.Release{}
			if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
				t.Fatal(err)
			}
			if controllerutil.ContainsFinalizer(release, global.FinalizerName) != c.finalizer {
				t.Fatalf("got the finalizers %q, want the one of KuboCD: %t", release.Finalizers, c.finalizer)
			}
			for _, clusterConnectionName := range c.deleted {
				err := testbed.client.Get(context.Background(), types.NamespacedName{Name: clusterConnectionName}, &kv1alpha1.ClusterConnection{})
				if !apierrors.IsNotFound(err) {
					t.Errorf("%s: got %v, want it deleted", clusterConnectionName, err)
				}
			}
			for _, clusterConnectionName := range c.left {
				if err := testbed.client.Get(context.Background(), types.NamespacedName{Name: clusterConnectionName},
					&kv1alpha1.ClusterConnection{}); err != nil {
					t.Errorf("%s: got %v, want it still there", clusterConnectionName, err)
				}
			}
		})
	}
}

// A control: a folder that cannot be removed does not keep the finalizer.
func TestTheFinalizerOfAReleaseGoesWhenOnlyItsFolderStays(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext, deletingTheRelease(t))
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got the error %v, want none", got.err)
	}
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	if controllerutil.ContainsFinalizer(release, global.FinalizerName) {
		t.Fatalf("got the finalizers %q, want the one of KuboCD removed", release.Finalizers)
	}
}
