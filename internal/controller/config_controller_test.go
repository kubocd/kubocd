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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

// gatedStore holds each AddConfigs until the gate opens, and closes entered
// when the first one starts.
type gatedStore struct {
	configstore.ConfigStore
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *gatedStore) AddConfigs(list *kv1alpha1.ConfigList, namespace string) error {
	s.once.Do(func() { close(s.entered) })
	<-s.gate
	return s.ConfigStore.AddConfigs(list, namespace)
}

// configReconcilerOf is the reconciler of the Configs of the namespace
// kubocd, which holds the Config conf with the spec given.
func configReconcilerOf(t *testing.T, spec kv1alpha1.ConfigSpec, store configstore.ConfigStore,
	published chan event.GenericEvent) *ConfigReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	config := &kv1alpha1.Config{ObjectMeta: metav1.ObjectMeta{Namespace: "kubocd", Name: "conf"}, Spec: spec}
	return &ConfigReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build(),
		EventRecorder: record.NewFakeRecorder(10), Logger: logr.Discard(), ConfigStore: store, MyPodNamespace: "kubocd",
		Published: published}
}

var confRequest = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "kubocd", Name: "conf"}}

func TestTheConfigControllerSignalsAConfigurationOnceItIsPublished(t *testing.T) {
	spec := kv1alpha1.ConfigSpec{SpecPatch: &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"4m"}`)}}
	store := &gatedStore{ConfigStore: configstore.New(), gate: make(chan struct{}), entered: make(chan struct{})}
	var open sync.Once
	release := func() { open.Do(func() { close(store.gate) }) }
	t.Cleanup(release)
	published := make(chan event.GenericEvent, 1)
	r := configReconcilerOf(t, spec, store, published)
	done := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(context.Background(), confRequest)
		done <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the reconciliation does not publish")
	}
	select {
	case <-published:
		t.Fatal("the publication is signaled before the store holds it")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case e := <-published:
		if patch := store.GetSpecPatch(); store.Version() != 1 || patch == nil || string(patch.Raw) != `{"timeout":"4m"}` {
			t.Fatalf("got the version %d and the specPatch %v when the signal comes, want the published configuration",
				store.Version(), patch)
		}
		if e.Object == nil || e.Object.GetNamespace() != "kubocd" || e.Object.GetName() != "conf" {
			t.Fatalf("got the event object %v, want the Config kubocd/conf", e.Object)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no signal once the configuration is published")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAConfigurationThatDoesNotLoadIsNotSignaled(t *testing.T) {
	published := make(chan event.GenericEvent, 1)
	r := configReconcilerOf(t, kv1alpha1.ConfigSpec{DefaultOnFailureStrategy: "missing"}, configstore.New(), published)
	if _, err := r.Reconcile(context.Background(), confRequest); err == nil {
		t.Fatal("the Config with an undefined default strategy loads")
	}
	select {
	case <-published:
		t.Fatal("a configuration that did not load is signaled")
	default:
	}
}

func TestAPendingSignalDoesNotBlockTheConfigController(t *testing.T) {
	published := make(chan event.GenericEvent, 1)
	published <- event.GenericEvent{Object: &kv1alpha1.Config{}}
	r := configReconcilerOf(t, kv1alpha1.ConfigSpec{}, configstore.New(), published)
	done := make(chan error, 1)
	go func() {
		_, err := r.Reconcile(context.Background(), confRequest)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil || len(published) != 1 {
			t.Fatalf("got the error %v and %d signals waiting, want no error and the one already waiting", err, len(published))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the reconciliation blocks on the signal")
	}
}
