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
	"slices"
	"testing"

	"github.com/go-logr/logr/funcr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// reconciledContext is what one reconciliation of a Context left behind.
type reconciledContext struct {
	err     error
	context *kv1alpha1.Context
	events  []string
	log     string
}

// contextClient is a fake API server that holds objects.
func contextClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&kv1alpha1.Context{}).Build()
}

// reconcileContext reconciles once the Context apps/name on the API server of
// k8sClient.
func reconcileContext(t *testing.T, k8sClient client.Client, name string) reconciledContext {
	t.Helper()
	log := &logBuffer{}
	recorder := record.NewFakeRecorder(100)
	reconciler := &ContextReconciler{Client: k8sClient, EventRecorder: recorder, Logger: funcr.New(log.write, funcr.Options{Verbosity: 2})}
	key := types.NamespacedName{Namespace: "apps", Name: name}
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	got := reconciledContext{err: err, context: &kv1alpha1.Context{}, log: log.buf.String()}
	if err := k8sClient.Get(context.Background(), key, got.context); err != nil {
		t.Fatal(err)
	}
	for len(recorder.Events) > 0 {
		got.events = append(got.events, <-recorder.Events)
	}
	return got
}

// The direct call of Reconcile has no recovery: a panic stops the test binary.
func TestAContextWithoutValuesIsReady(t *testing.T) {
	parent := &kv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "parent"}}
	child := &kv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "child"},
		Spec: kv1alpha1.ContextSpec{Parents: []kv1alpha1.NamespacedName{{Name: "parent"}},
			Context: &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"pg"}}`)}}}
	k8sClient := contextClient(t, parent, child)
	got := reconcileContext(t, k8sClient, "parent")
	if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady {
		t.Fatalf("got error %v and phase %q of the Context without values, want READY", got.err, got.context.Status.Phase)
	}
	got = reconcileContext(t, k8sClient, "child")
	if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || got.context.Status.Context == nil ||
		string(got.context.Status.Context.Raw) != `{"db":{"host":"pg"}}` {
		t.Fatalf("got error %v, phase %q and context %v of the child, want READY with its own values", got.err,
			got.context.Status.Phase, got.context.Status.Context)
	}
}

// refusingParentReads refuses, as RBAC would, to read the Context parent.
func refusingParentReads(base client.WithWatch) client.WithWatch {
	return interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "parent" {
				return apierrors.NewForbidden(schema.GroupResource{Group: kv1alpha1.GroupVersion.Group, Resource: "contexts"}, key.Name,
					errors.New("access denied"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
}

func TestAMissingParentIsToldFromAFailedRead(t *testing.T) {
	const forbidden = `contexts.kubocd.kubotal.io "parent" is forbidden: access denied`
	child := &kv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "child"},
		Spec: kv1alpha1.ContextSpec{Parents: []kv1alpha1.NamespacedName{{Name: "parent"}}}}
	missing := reconcileContext(t, contextClient(t, child.DeepCopy()), "child")
	if missing.err != nil || !slices.Equal(missing.events, []string{"Warning MissingParent parent 'apps:parent' not found"}) {
		t.Fatalf("missing parent: got error %v and events %q, want no error and the MissingParent event", missing.err, missing.events)
	}
	parent := &kv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "parent"}}
	refused := reconcileContext(t, refusingParentReads(contextClient(t, child.DeepCopy(), parent)), "child")
	want := "could not get parent 'apps:parent': " + forbidden
	var rErr ReconcileError
	if refused.err == nil || refused.err.Error() != want || !errors.As(refused.err, &rErr) || !apierrors.IsForbidden(rErr.GetBaseError()) ||
		!slices.Equal(refused.events, []string{"Warning GetParent " + want}) {
		t.Fatalf("refused read: got error %v and events %q, want the error %q and the GetParent event", refused.err, refused.events, want)
	}
	for name, got := range map[string]reconciledContext{"missing parent": missing, "refused read": refused} {
		if got.context.Status.Phase != kv1alpha1.ContextPhaseError {
			t.Errorf("%s: got phase %q, want ERROR", name, got.context.Status.Phase)
		}
	}
}
