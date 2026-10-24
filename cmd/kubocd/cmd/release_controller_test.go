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
package cmd

import (
	"context"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// fluxCRD is a CRD of a namespaced kind of Flux that keeps every field: the
// watches of the Release controller need the kind only.
func fluxCRD(group string, version string, kind string, plural string) *apiextensionsv1.CustomResourceDefinition {
	keep := true
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: plural + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind, ListKind: kind + "List", Plural: plural,
				Singular: strings.ToLower(kind)},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: version, Served: true, Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Type: "object",
					XPreserveUnknownFields: &keep}},
				Subresources: &apiextensionsv1.CustomResourceSubresources{
					Status: &apiextensionsv1.CustomResourceSubresourceStatus{}}}},
		},
	}
}

// gatedPublications holds each AddConfigs on its gate once one is set.
type gatedPublications struct {
	configstore.ConfigStore
	mu   sync.Mutex
	gate chan struct{}
	open sync.Once
}

func (s *gatedPublications) hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gate = make(chan struct{})
}

func (s *gatedPublications) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil {
		s.open.Do(func() { close(s.gate) })
	}
}

func (s *gatedPublications) AddConfigs(list *kubocdv1alpha1.ConfigList, namespace string) error {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return s.ConfigStore.AddConfigs(list, namespace)
}

// versionRecorder records, at each reconciliation of a Release, the version
// of the configuration in effect.
type versionRecorder struct {
	store    configstore.ConfigStore
	mu       sync.Mutex
	versions []uint64
}

func (r *versionRecorder) Reconcile(_ context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.NamespacedName == (types.NamespacedName{Namespace: "team", Name: "site"}) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.versions = append(r.versions, r.store.Version())
	}
	return ctrl.Result{}, nil
}

func (r *versionRecorder) seen() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.versions)
}

func TestAReleaseIsReconciledAgainOnceAChangeOfTheConfigsIsPublished(t *testing.T) {
	server := startAPIServer(t, []string{"kubocd", "team"},
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.OCIRepositoryKind, "ocirepositories"),
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.HelmRepositoryKind, "helmrepositories"),
		fluxCRD(fluxv2.GroupVersion.Group, fluxv2.GroupVersion.Version, fluxv2.HelmReleaseKind, "helmreleases"))
	for _, add := range []func(*runtime.Scheme) error{sourcev1.AddToScheme, fluxv2.AddToScheme} {
		if err := add(server.scheme); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	config := &kubocdv1alpha1.Config{ObjectMeta: metav1.ObjectMeta{Namespace: "kubocd", Name: "conf"}}
	if err := server.client.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := server.client.Create(ctx, releaseOf("team", "site")); err != nil {
		t.Fatal(err)
	}
	store := &gatedPublications{ConfigStore: configstore.New()}
	if err := store.Init(ctx, server.client, "kubocd"); err != nil {
		t.Fatal(err)
	}
	// controller-runtime refuses two controllers of one name in a process, and the tests name them as Run does
	skipNameValidation := true
	mgr, err := ctrl.NewManager(server.cfg, ctrl.Options{Scheme: server.scheme, HealthProbeBindAddress: "0",
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: crconfig.Controller{SkipNameValidation: &skipNameValidation}})
	if err != nil {
		t.Fatal(err)
	}
	if err := indexReleases(mgr.GetFieldIndexer()); err != nil {
		t.Fatal(err)
	}
	published := make(chan event.GenericEvent, 1)
	configReconciler := &controller.ConfigReconciler{Client: mgr.GetClient(), EventRecorder: record.NewFakeRecorder(10),
		Logger: logr.Discard(), ConfigStore: store, MyPodNamespace: "kubocd", Published: published}
	err = ctrl.NewControllerManagedBy(mgr).For(&kubocdv1alpha1.Config{}).Named("kubocd-config").Complete(configReconciler)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &versionRecorder{store: store}
	if err := releaseController(mgr, recorder, store, published, logr.Discard()); err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := mgr.Start(managerCtx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	// Cleanups run in reverse order: this one stops the manager before the API server stops
	t.Cleanup(func() {
		store.release()
		cancel()
		<-stopped
	})
	// The Config controller publishes the Config of the start once more
	eventually(t, "the start of both controllers", 30*time.Second, func() bool {
		return store.Version() == 2 && len(recorder.seen()) > 0
	})
	eventually(t, "a second without reconciliation", 30*time.Second, func() bool {
		settled := len(recorder.seen())
		time.Sleep(time.Second)
		return len(recorder.seen()) == settled
	})

	store.hold()
	before := len(recorder.seen())
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := server.client.Get(ctx, types.NamespacedName{Namespace: "kubocd", Name: "conf"}, config); err != nil {
			return err
		}
		config.Spec.SpecPatch = &apiextensionsv1.JSON{Raw: []byte(`{"timeout":"4m"}`)}
		return server.client.Update(ctx, config)
	})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the reconciliation that the event of the Config gives", 10*time.Second, func() bool {
		return len(recorder.seen()) > before
	})
	if got := recorder.seen()[before:]; slices.ContainsFunc(got, func(v uint64) bool { return v != 2 }) {
		t.Fatalf("got the versions %v while the publication is held, want 2 only", got)
	}
	store.release()
	eventually(t, "a reconciliation of the Release with the published configuration", 10*time.Second, func() bool {
		return slices.Contains(recorder.seen(), 3)
	})
	t.Logf("versions of the configuration at each reconciliation of the Release: %v", recorder.seen())
}
