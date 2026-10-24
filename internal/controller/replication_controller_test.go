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
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const replicationCanary = "s3cr3t-replication-canary"

// replicationKey names the Replication apps/rep of the tests.
var replicationKey = types.NamespacedName{Namespace: "apps", Name: "rep"}

// refusal is an error of a request, whose message quotes the canary as the
// message of an admission policy or of a failed connection can.
type refusal struct {
	err  error
	want string
}

var refusals = map[string]refusal{
	"answer of the API server": {apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "db",
		errors.New("denied by policy, the value "+replicationCanary+" is not allowed")),
		"the API server answered Forbidden, " + apiWithheld},
	"error without status": {errors.New("dial tcp 10.0.0.1:443: " + replicationCanary),
		"the request to the API server failed, " + apiWithheld},
}

// replicatedObject returns an object of the kind given, Secret or ConfigMap,
// named namespace/db, holding value, and marked as replicated by apps/rep
// when replicated.
func replicatedObject(kind string, namespace string, value string, replicated bool) client.Object {
	meta := metav1.ObjectMeta{Namespace: namespace, Name: "db"}
	if replicated {
		meta.Annotations = map[string]string{ReplicationAnnotation: "apps/rep"}
	}
	if kind == KindConfigMap {
		return &corev1.ConfigMap{ObjectMeta: meta, Data: map[string]string{"password": value}}
	}
	return &corev1.Secret{ObjectMeta: meta, Data: map[string][]byte{"password": []byte(value)}}
}

// replicationOf returns the Replication apps/rep of the source db of the kind
// given, to the namespace given, with the finalizer when finalized.
func replicationOf(kind string, namespace string, finalized bool) *kv1alpha1.Replication {
	replication := &kv1alpha1.Replication{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "rep"},
		Spec: kv1alpha1.ReplicationSpec{Source: kv1alpha1.ReplicationSource{Kind: kind, Name: "db"},
			Destination: kv1alpha1.ReplicationDestination{Namespace: namespace}}}
	if finalized {
		replication.Finalizers = []string{global.FinalizerName}
	}
	return replication
}

// replicatingToOther is the Replication apps/rep that already replicated its
// source to other/db.
func replicatingToOther(kind string, namespace string) *kv1alpha1.Replication {
	replication := replicationOf(kind, namespace, true)
	replication.Status = kv1alpha1.ReplicationStatus{Phase: kv1alpha1.ReplicationPhaseReady, DestinationName: "db",
		Destination: &kv1alpha1.ReplicatedResource{Kind: kind, Namespace: "other", Name: "db"}}
	return replication
}

// deletedReplication is the Replication apps/rep under deletion, which
// replicated its source to other/db.
func deletedReplication(kind string) *kv1alpha1.Replication {
	replication := replicatingToOther(kind, "other")
	now := metav1.Now()
	replication.DeletionTimestamp = &now
	return replication
}

// kindOf returns the kind of a Secret or of a ConfigMap, and "" otherwise.
func kindOf(obj client.Object) string {
	switch obj.(type) {
	case *corev1.Secret:
		return KindSecret
	case *corev1.ConfigMap:
		return KindConfigMap
	}
	return ""
}

// isReplicated tells whether obj is the object of the kind given named
// namespace/db.
func isReplicated(obj client.Object, kind string, namespace string) bool {
	return kindOf(obj) == kind && obj.GetNamespace() == namespace && obj.GetName() == "db"
}

// replicationRequest is a request that a Replication makes, refused by the
// interceptor that funcs returns, which counts the refusals in reached.
type replicationRequest struct {
	objects func(kind string) []client.Object
	funcs   func(kind string, err error, reached *int) interceptor.Funcs
	want    func(kind string) string
	// status tells whether the status of the Replication holds the error
	status bool
}

func refusedGet(namespace string) func(kind string, err error, reached *int) interceptor.Funcs {
	return func(kind string, err error, reached *int) interceptor.Funcs {
		return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
			opts ...client.GetOption) error {
			if kindOf(obj) == kind && key.Namespace == namespace && key.Name == "db" {
				*reached++
				return err
			}
			return c.Get(ctx, key, obj, opts...)
		}}
	}
}

func refusedDelete(kind string, err error, reached *int) interceptor.Funcs {
	return interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		if isReplicated(obj, kind, "other") {
			*reached++
			return err
		}
		return c.Delete(ctx, obj, opts...)
	}}
}

func refusedReplicationUpdate(_ string, err error, reached *int) interceptor.Funcs {
	return interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*kv1alpha1.Replication); ok {
			*reached++
			return err
		}
		return c.Update(ctx, obj, opts...)
	}}
}

// the objects of a Replication of the source db to other, before its first
// replication, after it, after it with another destination, and under
// deletion.
func toReplicate(kind string) []client.Object {
	return []client.Object{replicatedObject(kind, "apps", replicationCanary, false), replicationOf(kind, "other", true)}
}

func replicated(kind string) []client.Object {
	return []client.Object{replicatedObject(kind, "apps", replicationCanary, false), replicatedObject(kind, "other", "old", true),
		replicatingToOther(kind, "other")}
}

func movedToThird(kind string) []client.Object {
	return []client.Object{replicatedObject(kind, "apps", replicationCanary, false), replicatedObject(kind, "other", "old", true),
		replicatingToOther(kind, "third")}
}

func deleted(kind string) []client.Object {
	return []client.Object{replicatedObject(kind, "apps", replicationCanary, false), replicatedObject(kind, "other", "old", true),
		deletedReplication(kind)}
}

func TestARefusedRequestOfAReplicationKeepsItsMessageOut(t *testing.T) {
	requests := map[string]replicationRequest{
		"create of the destination": {toReplicate, func(kind string, err error, reached *int) interceptor.Funcs {
			return interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if isReplicated(obj, kind, "other") {
					*reached++
					return err
				}
				return c.Create(ctx, obj, opts...)
			}}
		}, func(kind string) string { return "unable to create " + kind + " 'other/db': " }, true},
		"read of the source": {toReplicate, refusedGet("apps"),
			func(kind string) string { return "unable to fetch source " + kind + " 'apps/db': " }, true},
		"read of the destination": {toReplicate, refusedGet("other"),
			func(kind string) string { return "unable to fetch " + kind + " 'other/db': " }, true},
		"update of the destination": {replicated, func(kind string, err error, reached *int) interceptor.Funcs {
			return interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if isReplicated(obj, kind, "other") {
					*reached++
					return err
				}
				return c.Update(ctx, obj, opts...)
			}}
		}, func(kind string) string { return "unable to update " + kind + " 'other/db': " }, true},
		"read of the previous destination": {movedToThird, refusedGet("other"),
			func(kind string) string { return "unable to fetch " + kind + " 'other/db': " }, true},
		"deletion of the previous destination": {movedToThird, refusedDelete,
			func(kind string) string { return "unable to delete " + kind + " 'other/db': " }, true},
		"read of the destination of a deleted Replication": {deleted, refusedGet("other"),
			func(kind string) string { return "unable to fetch " + kind + " 'other/db': " }, false},
		"deletion of the destination of a deleted Replication": {deleted, refusedDelete,
			func(kind string) string { return "unable to delete " + kind + " 'other/db': " }, false},
		"removal of the finalizer": {deleted, refusedReplicationUpdate,
			func(string) string { return "could not remove the finalizer of the Replication: " }, false},
		"addition of the finalizer": {func(kind string) []client.Object {
			return []client.Object{replicatedObject(kind, "apps", replicationCanary, false), replicationOf(kind, "other", false)}
		}, refusedReplicationUpdate, func(string) string { return "could not add the finalizer to the Replication: " }, false},
		"update of the status": {toReplicate, func(_ string, err error, reached *int) interceptor.Funcs {
			return interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				*reached++
				return err
			}}
		}, func(string) string { return "could not update the Replication status: " }, false},
		"read of the Replication": {toReplicate, func(_ string, err error, reached *int) interceptor.Funcs {
			return interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object,
				opts ...client.GetOption) error {
				if _, ok := obj.(*kv1alpha1.Replication); ok {
					*reached++
					return err
				}
				return c.Get(ctx, key, obj, opts...)
			}}
		}, func(string) string { return "could not read the Replication: " }, false},
	}
	for name, request := range requests {
		for _, kind := range []string{KindSecret, KindConfigMap} {
			for refusalName, refused := range refusals {
				t.Run(name+", "+kind+", "+refusalName, func(t *testing.T) {
					scheme := runtime.NewScheme()
					if err := clientgoscheme.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					if err := kv1alpha1.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(request.objects(kind)...).
						WithStatusSubresource(&kv1alpha1.Replication{}).Build()
					log := &logBuffer{}
					recorder := record.NewFakeRecorder(100)
					reached := 0
					r := &ReplicationReconciler{Client: interceptor.NewClient(base, request.funcs(kind, refused.err, &reached)),
						EventRecorder: recorder, Logger: funcr.New(log.write, funcr.Options{Verbosity: 2})}
					_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: replicationKey})
					want := request.want(kind) + refused.want
					if reached == 0 || err == nil || err.Error() != want {
						t.Fatalf("got %d refusals and the error %v, want the refusal and %q", reached, err, want)
					}
					got := &kv1alpha1.Replication{}
					if err := base.Get(context.Background(), replicationKey, got); err != nil {
						t.Fatal(err)
					}
					if request.status && (got.Status.Phase != kv1alpha1.ReplicationPhaseError || got.Status.Message != want) {
						t.Errorf("got the phase %q and the message %q, want ERROR and %q", got.Status.Phase, got.Status.Message, want)
					}
					if strings.Contains(got.Status.Message, replicationCanary) {
						t.Errorf("the status quotes the error of the request: %q", got.Status.Message)
					}
					for len(recorder.Events) > 0 {
						if event := <-recorder.Events; strings.Contains(event, replicationCanary) {
							t.Errorf("the event %q quotes the error of the request", event)
						}
					}
					if strings.Contains(log.buf.String(), replicationCanary) {
						t.Errorf("the log quotes the error of the request:\n%s", log.buf.String())
					}
				})
			}
		}
	}
}
