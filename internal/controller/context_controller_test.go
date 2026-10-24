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

// statusWrites counts the status updates of the Context apps/child. With
// encodedAs set, it serves the status context of the child in that encoding,
// as an API server can, the stored status being unchanged.
type statusWrites struct {
	count     int
	encodedAs string
}

func (w *statusWrites) client(base client.WithWatch) client.WithWatch {
	return interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, obj, opts...); err != nil {
				return err
			}
			if kcontext, ok := obj.(*kv1alpha1.Context); ok && key.Name == "child" && w.encodedAs != "" {
				kcontext.Status.Context = &apiextensionsv1.JSON{Raw: []byte(w.encodedAs)}
			}
			return nil
		},
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object,
			opts ...client.SubResourceUpdateOption) error {
			if subResource == "status" && obj.GetName() == "child" {
				w.count++
			}
			return c.SubResource(subResource).Update(ctx, obj, opts...)
		},
	})
}

// childOfParent is a READY Context parent with values, and a Context child of
// it with values of its own.
func childOfParent() (*kv1alpha1.Context, *kv1alpha1.Context) {
	parent := contextWithParents("parent")
	parent.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"pg"}}`)}
	child := contextWithParents("child", "parent")
	child.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(`{"z":1,"a":2}`)}
	return ready(parent), child
}

func TestAContextWritesItsStatusOnlyWhenItChanges(t *testing.T) {
	parent, child := childOfParent()
	writes := &statusWrites{}
	other := contextWithParents("other")
	other.Spec.Context = nil
	base := contextClient(t, parent, child, ready(other))
	k8sClient := writes.client(base)
	for range 3 {
		if got := reconcileContext(t, k8sClient, "child"); got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady {
			t.Fatalf("got error %v and phase %q, want READY", got.err, got.context.Status.Phase)
		}
	}
	if writes.count != 1 {
		t.Fatalf("three reconciliations wrote the status %d times, want once", writes.count)
	}

	for _, encoding := range []string{`{ "z": 1, "db": { "host": "pg" }, "a": 2 }`, `{"a":2.0,"db":{"host":"pg"},"z":1e0}`} {
		writes.encodedAs = encoding
		got := reconcileContext(t, k8sClient, "child")
		if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || writes.count != 1 {
			t.Fatalf("the same values encoded as %s gave the error %v, the phase %q and %d writes, want READY and no "+
				"new write", encoding, got.err, got.context.Status.Phase, writes.count)
		}
	}
	writes.encodedAs = ""

	stored := &kv1alpha1.Context{}
	if err := base.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "child"}, stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Parents = append(stored.Spec.Parents, kv1alpha1.NamespacedName{Namespace: "apps", Name: "other"})
	if err := base.Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	if got := reconcileContext(t, k8sClient, "child"); writes.count != 2 || got.context.Status.Parents != "apps:parent,apps:other" {
		t.Fatalf("a new parent without values gave %d writes and the parents %q, want 2 writes and both parents", writes.count,
			got.context.Status.Parents)
	}

	if err := base.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "parent"}, stored); err != nil {
		t.Fatal(err)
	}
	stored.Status.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"pg2"}}`)}
	if err := base.Status().Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	got := reconcileContext(t, k8sClient, "child")
	if writes.count != 3 || string(got.context.Status.Context.Raw) != `{"a":2,"db":{"host":"pg2"},"z":1}` {
		t.Fatalf("new values of the parent gave %d writes and the context %s, want 3 writes and the new values", writes.count,
			got.context.Status.Context.Raw)
	}
}

func TestAContextInErrorWritesItsStatusOnceReady(t *testing.T) {
	parent, child := childOfParent()
	child.Status = kv1alpha1.ContextStatus{Phase: kv1alpha1.ContextPhaseError, Parents: "apps:parent",
		Context: &apiextensionsv1.JSON{Raw: []byte(`{"a":2,"db":{"host":"pg"},"z":1}`)}}
	writes := &statusWrites{}
	got := reconcileContext(t, writes.client(contextClient(t, parent, child)), "child")
	if got.err != nil || got.context.Status.Phase != kv1alpha1.ContextPhaseReady || writes.count != 1 {
		t.Fatalf("got error %v, phase %q and %d writes, want READY written once", got.err, got.context.Status.Phase, writes.count)
	}
}

func TestTheValuesOfAContextAreComparedAsJSONValues(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{`{"n":1}`, `{"n":1.0}`, true},
		{`{"n":1}`, `{"n":1e0}`, true},
		{`{"n":0.5}`, `{"n":5e-1}`, true},
		{`{"n":1}`, `{"n":2}`, false},
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`, false},
		{`{"l":[1,2]}`, `{"l":[1,2]}`, true},
		{`{"l":[1,2]}`, `{"l":[2,1]}`, false},
		{`{"l":[1,2]}`, `{"l":[1,2,3]}`, false},
		{`{"a":null}`, `{"b":null}`, false},
		{`{"a":null}`, `{}`, false},
		{`{"s":"1"}`, `{"s":1}`, false},
		{`{"a":{"b":true}}`, `{ "a" : { "b" : true } }`, true},
	}
	for _, c := range cases {
		if got := sameJSON(&apiextensionsv1.JSON{Raw: []byte(c.a)}, &apiextensionsv1.JSON{Raw: []byte(c.b)}); got != c.same {
			t.Errorf("%s and %s compared as the same: %v, want %v", c.a, c.b, got, c.same)
		}
	}
	if !sameJSON(nil, nil) || sameJSON(nil, &apiextensionsv1.JSON{Raw: []byte(`{}`)}) {
		t.Error("a missing value is the same as a missing one only")
	}
}
