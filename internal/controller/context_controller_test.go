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
	"strings"
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

// contextIn is the Context namespace/name, whose parents are given as
// namespace:name, or name for the namespace of the Context.
func contextIn(namespace string, name string, parents ...string) *kv1alpha1.Context {
	kcontext := &kv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: kv1alpha1.ContextSpec{Context: &apiextensionsv1.JSON{Raw: []byte(`{"` + name + `":"x"}`)}}}
	for _, parent := range parents {
		ref := kv1alpha1.NamespacedName{Name: parent}
		if parentNamespace, parentName, ok := strings.Cut(parent, ":"); ok {
			ref = kv1alpha1.NamespacedName{Namespace: parentNamespace, Name: parentName}
		}
		kcontext.Spec.Parents = append(kcontext.Spec.Parents, ref)
	}
	return kcontext
}

// contextWithParents is the Context apps/name, whose parents are in apps.
func contextWithParents(name string, parents ...string) *kv1alpha1.Context {
	return contextIn("apps", name, parents...)
}

// withPhase gives the Context the status of an earlier reconciliation.
func withPhase(phase kv1alpha1.ContextPhase, kcontext *kv1alpha1.Context) *kv1alpha1.Context {
	kcontext.Status = kv1alpha1.ContextStatus{Phase: phase, Context: kcontext.Spec.Context}
	return kcontext
}

func ready(kcontext *kv1alpha1.Context) *kv1alpha1.Context {
	return withPhase(kv1alpha1.ContextPhaseReady, kcontext)
}

func TestACycleOfParentsIsReported(t *testing.T) {
	cases := map[string]struct {
		contexts []client.Object
		want     string
	}{
		"new cycle": {[]client.Object{contextWithParents("a", "b"), contextWithParents("b", "a")},
			"parents form a cycle: apps:a -> apps:b -> apps:a"},
		"cycle formed once both are READY": {[]client.Object{ready(contextWithParents("a", "b")), ready(contextWithParents("b", "a"))},
			"parents form a cycle: apps:a -> apps:b -> apps:a"},
		"cycle through a grandparent": {[]client.Object{contextWithParents("a", "c", "b"), ready(contextWithParents("b", "d")),
			ready(contextWithParents("c")), ready(contextWithParents("d", "a"))},
			"parents form a cycle: apps:a -> apps:b -> apps:d -> apps:a"},
		"cycle across two namespaces": {[]client.Object{contextIn("apps", "a", "other:b"), ready(contextIn("other", "b", "c")),
			ready(contextIn("other", "c", "apps:a")), ready(contextIn("apps", "c"))},
			"parents form a cycle: apps:a -> other:b -> other:c -> apps:a"},
		"parent of itself": {[]client.Object{ready(contextWithParents("a", "a"))}, "parents form a cycle: apps:a -> apps:a"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := reconcileContext(t, contextClient(t, c.contexts...), "a")
			if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseError ||
				!slices.Equal(got.events, []string{"Warning ParentCycle " + c.want}) {
				t.Fatalf("got error %v, phase %q and events %q, want ERROR and the event %q", got.err, got.context.Status.Phase,
					got.events, "Warning ParentCycle "+c.want)
			}
		})
	}
}

func TestAContextIsReadyOnceItsCycleIsBroken(t *testing.T) {
	k8sClient := contextClient(t, contextWithParents("a", "b"), contextWithParents("b", "a"))
	if got := reconcileContext(t, k8sClient, "a"); got.context.Status.Phase != kv1alpha1.ContextPhaseError {
		t.Fatalf("got the phase %q of the cycle, want ERROR", got.context.Status.Phase)
	}
	b := &kv1alpha1.Context{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "b"}, b); err != nil {
		t.Fatal(err)
	}
	b.Spec.Parents = nil
	if err := k8sClient.Update(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := reconcileContext(t, k8sClient, "b"); got.context.Status.Phase != kv1alpha1.ContextPhaseReady {
		t.Fatalf("got the phase %q of b without parent, want READY", got.context.Status.Phase)
	}
	if got := reconcileContext(t, k8sClient, "a"); got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady {
		t.Fatalf("got error %v and phase %q once the cycle is broken, want READY", got.err, got.context.Status.Phase)
	}
}

func TestAContextAboveACycleGetsTheErrorOfItsParent(t *testing.T) {
	k8sClient := contextClient(t, contextWithParents("a", "b"), withPhase(kv1alpha1.ContextPhaseError, contextWithParents("b", "c")),
		withPhase(kv1alpha1.ContextPhaseError, contextWithParents("c", "b")))
	got := reconcileContext(t, k8sClient, "a")
	if got.err == nil || got.context.Status.Phase != kv1alpha1.ContextPhaseError ||
		!slices.Equal(got.events, []string{"Warning ParentError parent 'apps:b' is in error"}) {
		t.Fatalf("got error %v, phase %q and events %q, want ERROR and the ParentError event", got.err, got.context.Status.Phase,
			got.events)
	}
}

// A control: two paths to the same parent are no cycle.
func TestTwoPathsToTheSameParentAreNotACycle(t *testing.T) {
	k8sClient := contextClient(t, contextWithParents("a", "b", "c"), ready(contextWithParents("b", "d")),
		ready(contextWithParents("c", "d")), ready(contextWithParents("d")))
	got := reconcileContext(t, k8sClient, "a")
	if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || len(got.events) != 0 {
		t.Fatalf("got error %v, phase %q and events %q, want READY", got.err, got.context.Status.Phase, got.events)
	}
}
