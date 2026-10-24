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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// statusTestbed is a reconciler whose API server refuses the status updates
// of the Releases named in refused, as on a conflict.
type statusTestbed struct {
	t          *testing.T
	client     client.Client
	reconciler *ReleaseReconciler
	refused    map[types.NamespacedName]bool
	updates    int
}

func newStatusTestbed(t *testing.T, releases ...*kv1alpha1.Release) *statusTestbed {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := make([]client.Object, len(releases))
	for i := range releases {
		objects[i] = releases[i]
	}
	b := &statusTestbed{t: t, refused: map[types.NamespacedName]bool{}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&kv1alpha1.Release{}).Build()
	b.client = interceptor.NewClient(base, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if b.refused[client.ObjectKeyFromObject(obj)] {
				return apierrors.NewConflict(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "releases"}, obj.GetName(),
					errors.New("the object has been modified"))
			}
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	store := newTestStore(t)
	b.reconciler = &ReleaseReconciler{Client: b.client, Logger: logr.Discard(), ServerRoot: t.TempDir(), ConfigStore: store,
		RoleStore: rolestore.New(store, logr.Discard())}
	return b
}

func (b *statusTestbed) update(key types.NamespacedName) (ctrl.Result, error) {
	b.t.Helper()
	release := &kv1alpha1.Release{}
	if err := b.client.Get(context.Background(), key, release); err != nil {
		b.t.Fatal(err)
	}
	b.updates++
	op := &releaseOperation{request: ctrl.Request{NamespacedName: key}, ctx: context.Background(), logger: logr.Discard(), release: release}
	return b.reconciler.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmReleases, "update", true)
}

// hidden checks that the failed update of the Release is hidden and requeued.
func (b *statusTestbed) hidden(step string, key types.NamespacedName) {
	b.t.Helper()
	b.refused[key] = true
	if result, err := b.update(key); err != nil || result.RequeueAfter == 0 {
		b.t.Fatalf("%s: got result %v and error %v, want the failure hidden and requeued", step, result, err)
	}
}

func (b *statusTestbed) returned(step string, key types.NamespacedName) {
	b.t.Helper()
	b.refused[key] = true
	if _, err := b.update(key); err == nil {
		b.t.Fatalf("%s: got no error, want the failure returned", step)
	}
}

func (b *statusTestbed) written(step string, key types.NamespacedName) {
	b.t.Helper()
	b.refused[key] = false
	if result, err := b.update(key); err != nil || result.RequeueAfter != 0 {
		b.t.Fatalf("%s: got result %v and error %v, want the status written", step, result, err)
	}
}

var (
	releaseA      = types.NamespacedName{Namespace: "apps", Name: "a"}
	releaseB      = types.NamespacedName{Namespace: "apps", Name: "b"}
	otherReleaseA = types.NamespacedName{Namespace: "other", Name: "a"}
)

func TestEachReleaseHidesItsOwnFirstFailedStatusUpdate(t *testing.T) {
	b := newStatusTestbed(t, newTestRelease("a", ""), newTestRelease("b", ""))
	b.hidden("first failure of a", releaseA)
	b.hidden("first failure of b", releaseB)
	b.returned("second failure of a", releaseA)
	b.written("update of b", releaseB)
	b.returned("third failure of a, once b is written", releaseA)
	b.written("update of a", releaseA)
	b.hidden("failure of a once written", releaseA)
}

func TestTwoReleasesOfTheSameNameHideTheirFirstFailedStatusUpdate(t *testing.T) {
	other := newTestRelease("a", "")
	other.Namespace = "other"
	b := newStatusTestbed(t, newTestRelease("a", ""), other)
	b.hidden("first failure of apps/a", releaseA)
	b.hidden("first failure of other/a", otherReleaseA)
}

func TestTheReleaseThatReplacesADeletedOneHidesItsFirstFailedStatusUpdate(t *testing.T) {
	release := newTestRelease("a", "")
	release.Finalizers = []string{global.FinalizerName}
	b := newStatusTestbed(t, release)
	b.hidden("first failure", releaseA)
	if err := b.client.Delete(context.Background(), release.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: releaseA}); err != nil {
		t.Fatalf("deletion: got %v", err)
	}
	if err := b.client.Get(context.Background(), releaseA, &kv1alpha1.Release{}); !apierrors.IsNotFound(err) {
		t.Fatalf("got %v, want the Release deleted", err)
	}
	if err := b.client.Create(context.Background(), newTestRelease("a", "")); err != nil {
		t.Fatal(err)
	}
	b.hidden("first failure of the new Release", releaseA)
}
