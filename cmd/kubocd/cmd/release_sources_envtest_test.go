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

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const sourceRaceRevision = "source-race-revision"
const sourceRaceAnnotation = "kubocd.kubotal.io/package-revision"
const sourceRaceMarker = "replacement-preserved"

const (
	sourceRaceOciPatch    = "oci-patch"
	sourceRaceHelmPatch   = "helm-patch"
	sourceRaceReplace     = "replace"
	sourceRaceNamespace   = "apps"
	sourceRaceDelete      = "helm-delete"
	sourceRaceRepair      = "oci-repair"
	sourceRaceUnchanged   = "unchanged"
	sourceRaceOwnerChange = "owner"
)

func startSourceRaceAPI(t *testing.T) *apiServer {
	t.Helper()
	server := startAPIServer(t, []string{sourceRaceNamespace},
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.OCIRepositoryKind, "ocirepositories"),
		fluxCRD(sourcev1.GroupVersion.Group, sourcev1.GroupVersion.Version, sourcev1.HelmRepositoryKind, "helmrepositories"))
	if err := sourcev1.AddToScheme(server.scheme); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestFluxSourceReplacementWritesAreRejected(t *testing.T) {
	server := startSourceRaceAPI(t)
	for _, operation := range []string{sourceRaceOciPatch, sourceRaceHelmPatch, sourceRaceDelete} {
		t.Run(operation, func(t *testing.T) { runSourceWriteRace(t, server, operation, sourceRaceReplace) })
	}
}

func TestFluxSourceWritesToSameUIDStillWork(t *testing.T) {
	server := startSourceRaceAPI(t)
	for _, operation := range []string{sourceRaceOciPatch, sourceRaceHelmPatch, sourceRaceDelete} {
		for _, mode := range []string{sourceRaceUnchanged, "metadata"} {
			t.Run(operation+"/"+mode, func(t *testing.T) { runSourceWriteRace(t, server, operation, mode) })
		}
	}
}

func TestInsecureSourceRepairRaceIsRejected(t *testing.T) {
	server := startSourceRaceAPI(t)
	for _, mode := range []string{sourceRaceReplace, sourceRaceOwnerChange, sourceRaceUnchanged} {
		t.Run(mode, func(t *testing.T) { runSourceWriteRace(t, server, sourceRaceRepair, mode) })
	}
}

// sourceRaceClient changes API-server state immediately before forwarding a
// source write, retaining the controller's original object, patch and options.
type sourceRaceClient struct {
	client.Client
	before        func(client.Object)
	key           client.ObjectKey
	kind          reflect.Type
	attempts      int
	patch         []byte
	preconditions *metav1.Preconditions
	apiError      error
}

func (c *sourceRaceClient) targets(obj client.Object) bool {
	return client.ObjectKeyFromObject(obj) == c.key && reflect.TypeOf(obj) == c.kind
}

func (c *sourceRaceClient) Patch(
	ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption,
) error {
	if !c.targets(obj) {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}
	c.attempts++
	data, err := patch.Data(obj)
	if err != nil {
		return err
	}
	c.patch = data
	c.before(obj)
	c.apiError = c.Client.Patch(ctx, obj, patch, opts...)
	return c.apiError
}

func (c *sourceRaceClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if !c.targets(obj) {
		return c.Client.Delete(ctx, obj, opts...)
	}
	c.attempts++
	options := &client.DeleteOptions{}
	for _, opt := range opts {
		opt.ApplyToDelete(options)
	}
	c.preconditions = options.Preconditions.DeepCopy()
	c.before(obj)
	c.apiError = c.Client.Delete(ctx, obj, opts...)
	return c.apiError
}

type sourceRaceFixture struct {
	reconciler *controller.ReleaseReconciler
	recorder   *record.FakeRecorder
	release    *kv1alpha1.Release
	target     client.Object
	direct     client.Client
}

func newSourceRaceFixture(t *testing.T, server *apiServer, operation, mode string) *sourceRaceFixture {
	t.Helper()
	ctx := context.Background()
	store := configstore.New()
	if err := store.Init(ctx, server.client, sourceRaceNamespace); err != nil {
		t.Fatal(err)
	}
	release := releaseOf(sourceRaceNamespace, operation+"-"+mode)
	release.Finalizers = []string{global.FinalizerName}
	release.Spec.Package.Insecure = operation == sourceRaceRepair
	controller.GroomRelease(release, logr.Discard(), store)
	if err := server.client.Create(ctx, release); err != nil {
		t.Fatal(err)
	}
	if release.UID == "" {
		t.Fatal("API server did not assign a Release UID")
	}
	oci := &sourcev1.OCIRepository{ObjectMeta: metav1.ObjectMeta{
		Namespace: sourceRaceNamespace, Name: "kcd-" + release.Name}}
	if err := controller.PopulateOciRepository(oci, release, global.PackageContentMediaType, "extract", store,
		controller.OCIRepositoryOptions{AllowReleaseInsecureSource: true}); err != nil {
		t.Fatal(err)
	}
	if operation == sourceRaceOciPatch {
		oci.Spec.URL = "oci://old.example/package"
	}
	if err := ctrl.SetControllerReference(release, oci, server.scheme); err != nil {
		t.Fatal(err)
	}
	if err := server.client.Create(ctx, oci); err != nil {
		t.Fatal(err)
	}
	target := client.Object(oci)
	root := t.TempDir()
	if strings.HasPrefix(operation, "helm-") {
		setSourceRaceStatus(oci, true)
		if err := server.client.Status().Update(ctx, oci); err != nil {
			t.Fatal(err)
		}
		folder := filepath.Join(root, "hr", sourceRaceNamespace, release.Name)
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "revision.txt"), []byte(sourceRaceRevision), 0o600); err != nil {
			t.Fatal(err)
		}
		revision := sourceRaceRevision
		if operation == sourceRaceDelete {
			revision = "older-source-revision"
		}
		helm := &sourcev1.HelmRepository{ObjectMeta: metav1.ObjectMeta{
			Namespace: sourceRaceNamespace, Name: "kcd-" + release.Name,
			Annotations: map[string]string{sourceRaceAnnotation: revision}}}
		controller.PopulateHelmRepository(helm, release, "https://old.example/charts")
		if err := ctrl.SetControllerReference(release, helm, server.scheme); err != nil {
			t.Fatal(err)
		}
		if err := server.client.Create(ctx, helm); err != nil {
			t.Fatal(err)
		}
		target = helm
	}
	if err := server.client.Get(ctx, client.ObjectKeyFromObject(target), target); err != nil {
		t.Fatal(err)
	}
	if target.GetUID() == "" || target.GetResourceVersion() == "" {
		t.Fatal("source lacks API-assigned identity")
	}
	recorder := record.NewFakeRecorder(100)
	reconciler := &controller.ReleaseReconciler{Client: server.client, EventRecorder: recorder, Logger: logr.Discard(),
		ConfigStore: store, RoleStore: rolestore.New(store, logr.Discard()), ServerRoot: root,
		HelmRepoAdvAddr: "charts.example"}
	return &sourceRaceFixture{reconciler: reconciler, recorder: recorder, release: release,
		target: target, direct: server.client}
}

func setSourceRaceStatus(obj client.Object, ready bool) {
	condition := metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "TestFixture",
		Message: sourceRaceMarker, ObservedGeneration: obj.GetGeneration(), LastTransitionTime: metav1.Now()}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	artifact := &fluxmeta.Artifact{Revision: sourceRaceRevision, URL: "https://unused.example/artifact.tgz",
		Path: "artifact.tgz", Digest: "sha256:0123", LastUpdateTime: metav1.Now()}
	switch source := obj.(type) {
	case *sourcev1.OCIRepository:
		source.Status = sourcev1.OCIRepositoryStatus{ObservedGeneration: obj.GetGeneration(),
			Conditions: []metav1.Condition{condition}, Artifact: artifact}
	case *sourcev1.HelmRepository:
		source.Status = sourcev1.HelmRepositoryStatus{ObservedGeneration: obj.GetGeneration(),
			Conditions: []metav1.Condition{condition}, Artifact: artifact}
	}
}

// changeSourceRaceState uses a direct API client, including for deletion and
// replacement creation. There is no simulated API conflict.
func changeSourceRaceState(t *testing.T, b *sourceRaceFixture, mode string) client.Object {
	t.Helper()
	ctx := context.Background()
	obj := b.target.DeepCopyObject().(client.Object)
	key := client.ObjectKeyFromObject(obj)
	if err := b.direct.Get(ctx, key, obj); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case sourceRaceReplace:
		oldUID := obj.GetUID()
		if err := b.direct.Delete(ctx, obj, client.Preconditions{UID: &oldUID}); err != nil {
			t.Fatal(err)
		}
		if err := b.direct.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
			t.Fatalf("original source still present: %v", err)
		}
		obj.SetResourceVersion("")
		obj.SetUID("")
		obj.SetGeneration(0)
		obj.SetCreationTimestamp(metav1.Time{})
		obj.SetManagedFields(nil)
		refs := obj.GetOwnerReferences()
		refs[0].Name, refs[0].UID = "replacement-owner", "replacement-owner-uid"
		obj.SetOwnerReferences(refs)
		obj.SetLabels(map[string]string{"race": sourceRaceMarker})
		switch source := obj.(type) {
		case *sourcev1.OCIRepository:
			source.Spec.URL = "oci://replacement.example/package"
		case *sourcev1.HelmRepository:
			source.Spec.URL = "https://replacement.example/charts"
		}
		if err := b.direct.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		if obj.GetUID() == "" || obj.GetUID() == oldUID {
			t.Fatal("replacement must have a different API-assigned UID")
		}
	case sourceRaceOwnerChange:
		refs := obj.GetOwnerReferences()
		refs[0].Name, refs[0].UID = "concurrent-owner", "concurrent-owner-uid"
		obj.SetOwnerReferences(refs)
		if err := b.direct.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
	case "metadata":
		obj.SetLabels(map[string]string{"race": sourceRaceMarker})
		if err := b.direct.Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if mode != sourceRaceUnchanged {
		setSourceRaceStatus(obj, false)
		if err := b.direct.Status().Update(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.direct.Get(ctx, key, obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func runSourceWriteRace(t *testing.T, server *apiServer, operation, mode string) {
	t.Helper()
	b := newSourceRaceFixture(t, server, operation, mode)
	var expected client.Object
	stale := &sourceRaceClient{Client: server.client,
		key: client.ObjectKeyFromObject(b.target), kind: reflect.TypeOf(b.target)}
	stale.before = func(_ client.Object) {
		if stale.attempts != 1 {
			t.Fatalf("unexpected extra source write: %d", stale.attempts)
		}
		expected = changeSourceRaceState(t, b, mode)
	}
	b.reconciler.Client = stale
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(b.release)}
	_, err := b.reconciler.Reconcile(context.Background(), request)
	if stale.attempts != 1 || expected == nil {
		t.Fatalf("source write was not reached: attempts=%d err=%v", stale.attempts, err)
	}
	assertSourceRacePrecondition(t, stale, b.target, operation)
	rejected := mode == sourceRaceReplace || mode == sourceRaceOwnerChange
	if rejected {
		var status apierrors.APIStatus
		if !errors.As(stale.apiError, &status) ||
			(!apierrors.IsInvalid(stale.apiError) && !apierrors.IsConflict(stale.apiError)) {
			t.Fatalf("stale write must be refused by API server, got %T %v", stale.apiError, stale.apiError)
		}
		t.Logf("API server rejected stale %s: %s", operation, status.Status().Reason)
		reason := "OCIRepositoryPatchFailed"
		if operation == sourceRaceHelmPatch {
			reason = "HelmRepositoryPatchFailed"
		}
		if operation == sourceRaceDelete {
			reason = "HelmRepositoryDeleteFailed"
		}
		assertSourceRaceDiagnostic(t, b, err, reason, false)
		got := b.target.DeepCopyObject().(client.Object)
		if readErr := b.direct.Get(context.Background(), stale.key, got); readErr != nil {
			t.Fatal(readErr)
		}
		if !reflect.DeepEqual(got, expected) || !got.GetDeletionTimestamp().IsZero() {
			t.Fatalf("stale write changed the concurrent source: got %#v, want %#v", got, expected)
		}
		if operation == sourceRaceRepair {
			if err.Error() != "could not disable insecure transport on associated OCIRepository" {
				t.Errorf("repair diagnostic: %v", err)
			}
			_, retry := b.reconciler.Reconcile(context.Background(), request)
			assertSourceRaceDiagnostic(t, b, retry, "OCIRepositoryOwner", false)
			if stale.attempts != 1 {
				t.Error("owner rejection attempted a repair")
			}
		}
		return
	}
	assertSameUIDSourceWrite(t, b, stale, expected, operation, err)
}

func assertSameUIDSourceWrite(
	t *testing.T, b *sourceRaceFixture, stale *sourceRaceClient, expected client.Object, operation string, err error,
) {
	t.Helper()
	if stale.apiError != nil {
		t.Fatalf("same-UID API write failed: %v", stale.apiError)
	}
	if operation == sourceRaceRepair {
		assertSourceRaceDiagnostic(t, b, err, "ReleaseInsecureSource", true)
	} else if err != nil {
		t.Fatalf("same-UID reconciliation failed: %v", err)
	}
	got := b.target.DeepCopyObject().(client.Object)
	readErr := b.direct.Get(context.Background(), stale.key, got)
	if operation == sourceRaceDelete {
		if !apierrors.IsNotFound(readErr) {
			t.Fatalf("owned source was not deleted: %v", readErr)
		}
		return
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got.GetUID() != b.target.GetUID() || !reflect.DeepEqual(got.GetLabels(), expected.GetLabels()) ||
		!reflect.DeepEqual(got.GetOwnerReferences(), expected.GetOwnerReferences()) {
		t.Fatal("same-UID patch changed identity or unrelated metadata")
	}
	switch source := got.(type) {
	case *sourcev1.OCIRepository:
		if operation == sourceRaceRepair {
			if source.Spec.Insecure {
				t.Fatal("owned insecure source was not repaired")
			}
		} else if source.Spec.URL != "oci://"+b.release.Spec.Package.Repository {
			t.Fatalf("OCI URL not patched: %s", source.Spec.URL)
		}
		if !reflect.DeepEqual(source.Status, expected.(*sourcev1.OCIRepository).Status) {
			t.Error("patch changed source status")
		}
	case *sourcev1.HelmRepository:
		want := "http://charts.example/hr/apps/" + b.release.Name
		if source.Spec.URL != want {
			t.Fatalf("Helm URL=%q want %q", source.Spec.URL, want)
		}
		if !reflect.DeepEqual(source.Status, expected.(*sourcev1.HelmRepository).Status) {
			t.Error("patch changed source status")
		}
	}
}

func assertSourceRacePrecondition(t *testing.T, c *sourceRaceClient, original client.Object, operation string) {
	t.Helper()
	if operation == sourceRaceDelete {
		if c.preconditions == nil || c.preconditions.UID == nil || *c.preconditions.UID != original.GetUID() {
			t.Errorf("delete lacks original UID: %+v", c.preconditions)
		}
		return
	}
	var patch struct {
		Metadata struct {
			UID             string `json:"uid"`
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(c.patch, &patch); err != nil {
		t.Fatal(err)
	}
	if patch.Metadata.UID != string(original.GetUID()) {
		t.Errorf("patch lacks original UID: %s", c.patch)
	}
	if operation == sourceRaceRepair {
		if patch.Metadata.ResourceVersion != original.GetResourceVersion() {
			t.Errorf("repair lacks original resourceVersion: %s", c.patch)
		}
	} else if patch.Metadata.ResourceVersion != "" {
		t.Errorf("normal source patch unexpectedly locks resourceVersion: %s", c.patch)
	}
}

func assertSourceRaceDiagnostic(t *testing.T, b *sourceRaceFixture, err error, reason string, fatal bool) {
	t.Helper()
	if fatal {
		if err != nil {
			t.Fatalf("fatal refusal returned error: %v", err)
		}
	} else {
		rerr, ok := err.(controller.ReconcileError)
		if !ok || rerr.IsFatal() || rerr.GetEventReason() != reason {
			t.Fatalf("got %v, want retryable %s", err, reason)
		}
	}
	stored := &kv1alpha1.Release{}
	if readErr := b.direct.Get(context.Background(), client.ObjectKeyFromObject(b.release), stored); readErr != nil {
		t.Fatal(readErr)
	}
	if stored.Status.Phase != kv1alpha1.ReleasePhaseError || stored.Status.Message == "" {
		t.Fatalf("status=%+v", stored.Status)
	}
	if err != nil && stored.Status.Message != err.Error() {
		t.Errorf("status message differs from returned error: %q / %v", stored.Status.Message, err)
	}
	want := fmt.Sprintf("Warning %s %s", reason, stored.Status.Message)
	select {
	case event := <-b.recorder.Events:
		if event != want {
			t.Errorf("event=%q want %q", event, want)
		}
	default:
		t.Error("no warning event")
	}
}
