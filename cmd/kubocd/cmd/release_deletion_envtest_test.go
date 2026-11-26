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
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"os"
	"path/filepath"
	"testing"
	"time"

	kubocdv1alpha1 "kubocd/api/v1alpha1"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// uninstallFinalizer stands for the finalizer of helm-controller.
const uninstallFinalizer = "test.kubocd.kubotal.io/helm-uninstall"

// holdFinalizer stands for the finalizer of another controller, such as source-controller.
const holdFinalizer = "test.kubocd.kubotal.io/hold"

// deletionFixture is an API server with the Release apps/app and a reconciler that reads from a cache,
// as the controller does.
type deletionFixture struct {
	server     *apiServer
	reconciler *controller.ReleaseReconciler
	release    *kubocdv1alpha1.Release
	folder     string
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	server := startAPIServer(t, []string{"apps"},
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.OCIRepositoryKind, "ocirepositories"),
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.HelmRepositoryKind, "helmrepositories"),
		fluxCRD(fluxv2.GroupVersion.Group, fluxv2.GroupVersion.Version, fluxv2.HelmReleaseKind, "helmreleases"))
	for _, add := range []func(*runtime.Scheme) error{sourcev1.AddToScheme, fluxv2.AddToScheme} {
		if err := add(server.scheme); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	release := releaseOf("apps", "app")
	release.Finalizers = []string{global.FinalizerName}
	if err := server.client.Create(ctx, release); err != nil {
		t.Fatal(err)
	}
	owned := func(object client.Object) client.Object {
		if err := ctrl.SetControllerReference(release, object, server.scheme); err != nil {
			t.Fatal(err)
		}
		return object
	}
	main := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-main",
		Finalizers: []string{uninstallFinalizer}},
		Spec: fluxv2.HelmReleaseSpec{DependsOn: []fluxv2.DependencyReference{{Name: "app-oidc-client"}}}}
	oidcClient := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-oidc-client",
		Finalizers: []string{uninstallFinalizer}}}
	objects := []client.Object{
		owned(main), owned(oidcClient),
		owned(&sourcev1.HelmRepository{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "kcd-app",
			Finalizers: []string{holdFinalizer}}}),
		owned(&kubocdv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-db",
			Finalizers: []string{holdFinalizer}},
			Spec: kubocdv1alpha1.ClaimSpec{Contract: "postgres", Target: kubocdv1alpha1.ClaimTarget{Provisioner: "pg"}}}),
		owned(&kubocdv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-api",
			Finalizers: []string{holdFinalizer}}, Spec: kubocdv1alpha1.ConnectionSpec{Contract: "service"}}),
		&kubocdv1alpha1.ClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "kcd-apps-app-api",
			Finalizers: []string{holdFinalizer}}, Spec: kubocdv1alpha1.ClusterConnectionSpec{Contract: "service",
			ParentRelease: &kubocdv1alpha1.ParentReleaseRef{Namespace: "apps", Name: "app"}}},
		owned(&sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "kcd-app"}}),
	}
	for _, object := range objects {
		if err := server.client.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}

	informers, err := cache.New(server.cfg, cache.Options{Scheme: server.scheme})
	if err != nil {
		t.Fatal(err)
	}
	index := func(o client.Object) []string {
		return controller.OutputClusterConnectionReleaseKeys(o.(*kubocdv1alpha1.ClusterConnection))
	}
	indexName := controller.ReleaseIndexOnOutputClusterConnection
	err = informers.IndexField(ctx, &kubocdv1alpha1.ClusterConnection{}, indexName, index)
	if err != nil {
		t.Fatal(err)
	}
	informerCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	go func() { _ = informers.Start(informerCtx) }()
	cached, err := client.New(server.cfg, client.Options{Scheme: server.scheme,
		Cache: &client.CacheOptions{Reader: informers}})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	folder := filepath.Join(root, "hr", "apps", "app")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".transient", "revision.txt"} {
		if err := os.WriteFile(filepath.Join(folder, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := configstore.New()
	if err := store.AddConfigs(&kubocdv1alpha1.ConfigList{}, "kubocd"); err != nil {
		t.Fatal(err)
	}
	reconciler := &controller.ReleaseReconciler{Client: cached, APIReader: server.client,
		EventRecorder: record.NewFakeRecorder(100), Logger: logr.Discard(), ServerRoot: root, ConfigStore: store,
		RoleStore: rolestore.New(store, logr.Discard())}
	return &deletionFixture{server: server, reconciler: reconciler, release: release, folder: folder}
}

// state is "deleting", "present" or "gone".
func (f *deletionFixture) state(t *testing.T, object client.Object) string {
	t.Helper()
	err := f.server.client.Get(context.Background(), client.ObjectKeyFromObject(object), object)
	switch {
	case apierrors.IsNotFound(err):
		return "gone"
	case err != nil:
		t.Fatal(err)
	case !object.GetDeletionTimestamp().IsZero():
		return "deleting"
	}
	return "present"
}

func helmRelease(name string) *fluxv2.HelmRelease {
	return &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: name}}
}

// reconcileUntil reconciles the Release until done holds, and checks after each reconciliation that app-oidc-client
// is not deleted while app-main is there.
func (f *deletionFixture) reconcileUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	key := types.NamespacedName{Namespace: "apps", Name: "app"}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, _ = f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
		if f.state(t, helmRelease("app-main")) != "gone" && f.state(t, helmRelease("app-oidc-client")) != "present" {
			t.Fatal("app-oidc-client is deleted while app-main is there")
		}
		if done() {
			return
		}
	}
	t.Fatalf("timeout waiting for %s", what)
}

func (f *deletionFixture) endUninstall(t *testing.T, name string) {
	t.Helper()
	object := helmRelease(name)
	if err := f.server.client.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
		t.Fatal(err)
	}
	object.Finalizers = nil
	if err := f.server.client.Update(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}

func (f *deletionFixture) expectSources(t *testing.T, step string, want string) {
	t.Helper()
	for _, source := range []client.Object{&sourcev1.HelmRepository{}, &sourcev1.OCIRepository{}} {
		source.SetNamespace("apps")
		source.SetName("kcd-app")
		if got := f.state(t, source); got != want {
			t.Errorf("%s: the %T is %s, want %s", step, source, got, want)
		}
	}
	_, err := os.Stat(f.folder)
	if want == "present" && err != nil || want == "gone" && !os.IsNotExist(err) {
		t.Errorf("%s: got %v for the chart folder, want it %s", step, err, want)
	}
}

// TestADeletedReleaseUninstallsInOrder runs once for each child of the second step as the last one to go.
func TestADeletedReleaseUninstallsInOrder(t *testing.T) {
	for last, kind := range []string{"HelmRepository", "Claim", "Connection", "ClusterConnection"} {
		t.Run(kind+" last", func(t *testing.T) { deleteInOrder(t, last) })
	}
}

func deleteInOrder(t *testing.T, last int) {
	f := newDeletionFixture(t)
	ctx := context.Background()
	if err := f.server.client.Delete(ctx, f.release); err != nil {
		t.Fatal(err)
	}

	// The status of the Release was never written: DELETING is its first one
	f.reconcileUntil(t, "app-main deleted", func() bool {
		return f.state(t, helmRelease("app-main")) == "deleting"
	})
	release := &kubocdv1alpha1.Release{}
	if err := f.server.client.Get(ctx, client.ObjectKeyFromObject(f.release), release); err != nil {
		t.Fatal(err)
	}
	ready := apimeta.FindStatusCondition(release.Status.Conditions, kubocdv1alpha1.ReleaseConditionReady)
	if release.Status.Phase != kubocdv1alpha1.ReleasePhaseDeleting || ready == nil ||
		ready.Status != metav1.ConditionFalse || ready.Reason != "Deleting" ||
		ready.ObservedGeneration != release.Generation {
		t.Errorf("got the phase %s and the Ready condition %+v, want DELETING, False, Deleting, of the generation %d",
			release.Status.Phase, ready, release.Generation)
	}
	f.expectSources(t, "app-main uninstalling", "present")

	f.endUninstall(t, "app-main")
	f.reconcileUntil(t, "app-oidc-client deleted", func() bool {
		return f.state(t, helmRelease("app-oidc-client")) == "deleting"
	})
	f.expectSources(t, "app-oidc-client uninstalling", "present")

	f.endUninstall(t, "app-oidc-client")
	held := []client.Object{
		&sourcev1.HelmRepository{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "kcd-app"}},
		&kubocdv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-db"}},
		&kubocdv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "app-api"}},
		&kubocdv1alpha1.ClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "kcd-apps-app-api"}},
	}
	f.reconcileUntil(t, "the other children deleted", func() bool {
		for _, object := range held {
			if f.state(t, object) != "deleting" {
				return false
			}
		}
		return true
	})
	if got := f.state(t, &kubocdv1alpha1.Release{ObjectMeta: f.release.ObjectMeta}); got != "deleting" {
		t.Fatalf("the Release is %s while its children are deleting", got)
	}
	if _, err := os.Stat(f.folder); err != nil {
		t.Fatalf("got %v for the chart folder while the HelmRepository is deleting", err)
	}
	unhold := func(object client.Object) {
		if err := f.server.client.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatal(err)
		}
		object.SetFinalizers(nil)
		if err := f.server.client.Update(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	for i, object := range held {
		if i != last {
			unhold(object)
		}
	}
	f.reconcileUntil(t, "the others gone", func() bool {
		for i, object := range held {
			if i != last && f.state(t, object) != "gone" {
				return false
			}
		}
		return true
	})
	if got := f.state(t, &kubocdv1alpha1.Release{ObjectMeta: f.release.ObjectMeta}); got != "deleting" {
		t.Fatalf("the Release is %s while %T holds", got, held[last])
	}
	if _, err := os.Stat(f.folder); err != nil {
		t.Fatalf("got %v for the chart folder while %T holds", err, held[last])
	}
	unhold(held[last])
	f.reconcileUntil(t, "the Release gone", func() bool {
		return f.state(t, &kubocdv1alpha1.Release{ObjectMeta: f.release.ObjectMeta}) == "gone"
	})
	f.expectSources(t, "Release gone", "gone")
}
