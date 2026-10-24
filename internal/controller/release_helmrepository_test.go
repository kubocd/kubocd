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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// olderRevision is a revision of the package before reconcileRevision.
const olderRevision = "0.0.9@sha256:fedcba9876543210fedcba9876543210"

var helmRepositoryKey = types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(HelmRepositoryNameFormat, "demo")}

// packageArchive is a tar.gz of the files given, as source-controller serves
// the content of a package.
func packageArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// servedPackage serves the content of the package of the Release demo from an
// HTTP server, as source-controller does. It returns the files of the package
// and an option that points the OCIRepository of the Release to the server,
// with the digest of the archive, and gives the reconciler a fetcher that
// extracts it, as in production.
func servedPackage(t *testing.T, manifest string) (map[string]string, func(*ReleaseReconciler)) {
	t.Helper()
	files := map[string]string{"original.yaml": manifest,
		"status.yaml": "apiVersion: v1alpha1\nchartByModule:\n  main: {name: demo, version: 1.0.0}\n"}
	return files, servedArchive(t, packageArchive(t, files))
}

// servedArchive serves archive as the content of the package of the Release
// demo, as servedPackage does.
func servedArchive(t *testing.T, archive []byte) func(*ReleaseReconciler) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	return func(r *ReleaseReconciler) {
		r.Fetcher = NewPackageFetcher(DefaultPackageLimits, 0, "")
		source := &sourcev1.OCIRepository{}
		key := types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(OciRepositoryNameFormat, "demo")}
		if err := r.Get(context.Background(), key, source); err != nil {
			t.Fatal(err)
		}
		source.Status.Artifact.URL = server.URL + "/demo.tgz"
		source.Status.Artifact.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(archive))
		if err := r.Update(context.Background(), source); err != nil {
			t.Fatal(err)
		}
	}
}

// coldFolder removes the folders of the Releases, as a new pod of the
// controller finds them.
func coldFolder(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		if err := os.RemoveAll(filepath.Join(r.ServerRoot, "hr")); err != nil {
			t.Fatal(err)
		}
	}
}

// withHelmRepositoryAnnotations sets the annotations of the HelmRepository of
// the Release demo.
func withHelmRepositoryAnnotations(t *testing.T, annotations map[string]string) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		helmRepository := &sourcev1.HelmRepository{}
		if err := r.Get(context.Background(), helmRepositoryKey, helmRepository); err != nil {
			t.Fatal(err)
		}
		helmRepository.SetAnnotations(annotations)
		if err := r.Update(context.Background(), helmRepository); err != nil {
			t.Fatal(err)
		}
	}
}

// helmRepositoryWrites counts the deletions and the patches of a HelmRepository.
type helmRepositoryWrites struct {
	mutex            sync.Mutex
	deletes, patches int
}

func (w *helmRepositoryWrites) counts() (int, int) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.deletes, w.patches
}

// countingHelmRepositoryWrites counts the deletions and the patches of a
// HelmRepository asked by the reconciler.
func countingHelmRepositoryWrites(writes *helmRepositoryWrites) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*sourcev1.HelmRepository); ok {
					writes.mutex.Lock()
					writes.deletes++
					writes.mutex.Unlock()
				}
				return c.Delete(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if _, ok := obj.(*sourcev1.HelmRepository); ok {
					writes.mutex.Lock()
					writes.patches++
					writes.mutex.Unlock()
				}
				return c.Patch(ctx, obj, patch, opts...)
			},
		})
	}
}

// readyHelmRepository gives the HelmRepository of the Release demo the status
// that Flux gives it once it has read the index.
func readyHelmRepository(t *testing.T, testbed *releaseTestbed) {
	t.Helper()
	helmRepository := &sourcev1.HelmRepository{}
	if err := testbed.client.Get(context.Background(), helmRepositoryKey, helmRepository); err != nil {
		t.Fatal(err)
	}
	helmRepository.Status = sourcev1.HelmRepositoryStatus{
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Succeeded", LastTransitionTime: metav1.Now()}},
		Artifact:   &meta.Artifact{Revision: "sha256:0123", Digest: "sha256:0123", Path: "index.yaml", URL: "http://source/index.yaml", LastUpdateTime: metav1.Now()},
	}
	if err := testbed.client.Update(context.Background(), helmRepository); err != nil {
		t.Fatal(err)
	}
}

// phaseOf returns the phase of the Release demo.
func phaseOf(t *testing.T, testbed *releaseTestbed) kv1alpha1.ReleasePhase {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo"}, release); err != nil {
		t.Fatal(err)
	}
	return release.Status.Phase
}

// helmRepositoryAnnotation returns the revision annotation of the
// HelmRepository of the Release demo, and false when it does not exist.
func helmRepositoryAnnotation(t *testing.T, testbed *releaseTestbed) (string, bool) {
	t.Helper()
	helmRepository := &sourcev1.HelmRepository{}
	err := testbed.client.Get(context.Background(), helmRepositoryKey, helmRepository)
	if apierrors.IsNotFound(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return helmRepository.GetAnnotations()[packageRevisionAnnotation], true
}

// progressed tells whether the Release went past its HelmRepository without
// an error.
func progressed(t *testing.T, testbed *releaseTestbed, got reconciledRelease) bool {
	t.Helper()
	phase := phaseOf(t, testbed)
	return got.err == nil && phase != "" && phase != kv1alpha1.ReleasePhaseWaitHelmRepo && phase != kv1alpha1.ReleasePhaseError
}

// hasEvent tells whether one of the events has the reason given.
func hasEvent(events []string, reason string) bool {
	return slices.ContainsFunc(events, func(event string) bool { return strings.Contains(event, " "+reason+" ") })
}

func TestAColdStartKeepsTheHelmRepositoryOfAnUnchangedPackage(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	// Control: the same pass on a folder fetched by an earlier pass
	warm := newReleaseTestbed(t, manifest, "", afterRenderContext)
	want := warm.reconcile(t)

	files, served := servedPackage(t, manifest)
	writes := &helmRepositoryWrites{}
	cold := newReleaseTestbed(t, manifest, "", afterRenderContext, coldFolder(t), served, countingHelmRepositoryWrites(writes))
	got := cold.reconcile(t)

	folder := filepath.Join(cold.reconciler.ServerRoot, "hr", "apps", "demo")
	files["revision.txt"] = reconcileRevision
	for name, content := range files {
		if read, err := os.ReadFile(filepath.Join(folder, name)); err != nil || string(read) != content {
			t.Errorf("got %s %q (%v), want the content served %q", name, read, err, content)
		}
	}
	if deletes, _ := writes.counts(); deletes != 0 || hasEvent(got.events, "HelmRepositoryDeleted") {
		t.Errorf("got %d deletions of the HelmRepository and the events %q, want none", deletes, got.events)
	}
	if annotation, ok := helmRepositoryAnnotation(t, cold); !ok || annotation != reconcileRevision {
		t.Errorf("got the HelmRepository %v with the revision %q, want it with %q", ok, annotation, reconcileRevision)
	}
	if phaseOf(t, cold) != phaseOf(t, warm) || got.status != want.status || (got.err == nil) != (want.err == nil) {
		t.Errorf("got the phase %q, the status %q and the error %v, want those of a warm folder: %q, %q and %v",
			phaseOf(t, cold), got.status, got.err, phaseOf(t, warm), want.status, want.err)
	}
	if phaseOf(t, warm) == kv1alpha1.ReleasePhaseWaitHelmRepo || phaseOf(t, warm) == kv1alpha1.ReleasePhaseError {
		t.Fatalf("the control run stops at %q: %q", phaseOf(t, warm), want.status)
	}
	t.Logf("the cold start reaches %q: %q", phaseOf(t, cold), got.status)
}

func TestTheHelmRepositoryOfAnotherRevisionIsCreatedAgain(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	cases := map[string]struct {
		cold        bool
		annotations map[string]string
	}{
		"cold start, older revision": {true, map[string]string{packageRevisionAnnotation: olderRevision}},
		// The folder changed before a restart of the container, which keeps it
		"warm folder, older revision": {false, map[string]string{packageRevisionAnnotation: olderRevision}},
		// Created by a version that does not annotate its HelmRepository
		"no annotation": {true, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			writes := &helmRepositoryWrites{}
			_, served := servedPackage(t, manifest)
			options := []func(*ReleaseReconciler){served, withHelmRepositoryAnnotations(t, c.annotations),
				countingHelmRepositoryWrites(writes)}
			if c.cold {
				options = append([]func(*ReleaseReconciler){coldFolder(t)}, options...)
			}
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, options...)

			got := testbed.reconcile(t)
			_, exists := helmRepositoryAnnotation(t, testbed)
			if deletes, _ := writes.counts(); deletes != 1 || exists || !hasEvent(got.events, "HelmRepositoryDeleted") ||
				phaseOf(t, testbed) != kv1alpha1.ReleasePhaseWaitHelmRepo || got.err != nil {
				t.Fatalf("first pass: got %d deletions, the HelmRepository %v, the events %q, the phase %q and the error %v, "+
					"want it deleted and WAIT_REPO", deletes, exists, got.events, phaseOf(t, testbed), got.err)
			}

			got = testbed.reconcile(t)
			annotation, exists := helmRepositoryAnnotation(t, testbed)
			if !exists || annotation != reconcileRevision || !hasEvent(got.events, "HelmRepositoryCreated") {
				t.Fatalf("second pass: got the HelmRepository %v with the revision %q and the events %q, want it created with %q",
					exists, annotation, got.events, reconcileRevision)
			}

			readyHelmRepository(t, testbed)
			got = testbed.reconcile(t)
			if deletes, _ := writes.counts(); deletes != 1 || !progressed(t, testbed, got) {
				t.Fatalf("third pass: got %d deletions, the phase %q (%q) and the error %v, want the HelmRepository kept and "+
					"used", deletes, phaseOf(t, testbed), got.status, got.err)
			}

			// A new pod keeps it
			coldFolder(t)(testbed.reconciler)
			got = testbed.reconcile(t)
			if deletes, _ := writes.counts(); deletes != 1 || hasEvent(got.events, "HelmRepositoryDeleted") ||
				!progressed(t, testbed, got) {
				t.Fatalf("cold start: got %d deletions, the events %q, the phase %q (%q) and the error %v, want the "+
					"HelmRepository kept and used", deletes, got.events, phaseOf(t, testbed), got.status, got.err)
			}
		})
	}
}

func TestAHelmRepositoryBeingDeletedIsWaitedFor(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	for name, revision := range map[string]string{"same revision": reconcileRevision, "older revision": olderRevision} {
		t.Run(name, func(t *testing.T) {
			being := func(r *ReleaseReconciler) {
				helmRepository := &sourcev1.HelmRepository{}
				if err := r.Get(context.Background(), helmRepositoryKey, helmRepository); err != nil {
					t.Fatal(err)
				}
				helmRepository.SetAnnotations(map[string]string{packageRevisionAnnotation: revision})
				helmRepository.SetFinalizers([]string{"finalizers.fluxcd.io"})
				if err := r.Update(context.Background(), helmRepository); err != nil {
					t.Fatal(err)
				}
				if err := r.Delete(context.Background(), helmRepository); err != nil {
					t.Fatal(err)
				}
			}
			writes := &helmRepositoryWrites{}
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, being, countingHelmRepositoryWrites(writes))
			got := testbed.reconcile(t)
			deletes, patches := writes.counts()
			if deletes != 0 || patches != 0 || hasEvent(got.events, "HelmRepositoryDeleted") || got.err != nil ||
				phaseOf(t, testbed) != kv1alpha1.ReleasePhaseWaitHelmRepo || got.status != waitHelmRepository {
				t.Fatalf("got %d deletions, %d patches, the events %q, the error %v, the phase %q and the status %q, "+
					"want a wait", deletes, patches, got.events, got.err, phaseOf(t, testbed), got.status)
			}
		})
	}
}

func TestAHelmRepositoryThatTheCacheStillShowsIsNotDeletedTwice(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	stale := func(r *ReleaseReconciler) {
		helmRepository := &sourcev1.HelmRepository{}
		if err := r.Get(context.Background(), helmRepositoryKey, helmRepository); err != nil {
			t.Fatal(err)
		}
		helmRepository.SetAnnotations(map[string]string{packageRevisionAnnotation: olderRevision})
		if err := r.Delete(context.Background(), helmRepository); err != nil {
			t.Fatal(err)
		}
		// The cache of the manager still holds the HelmRepository deleted by an earlier pass
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if target, ok := obj.(*sourcev1.HelmRepository); ok && key == helmRepositoryKey {
					helmRepository.DeepCopyInto(target)
					return nil
				}
				return c.Get(ctx, key, obj, opts...)
			},
		})
	}
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, stale)
	got := testbed.reconcile(t)
	if got.err != nil || hasEvent(got.events, "HelmRepositoryDeleted") || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseWaitHelmRepo ||
		got.status != waitHelmRepository {
		t.Fatalf("got the error %v, the events %q, the phase %q and the status %q, want a wait", got.err, got.events,
			phaseOf(t, testbed), got.status)
	}
}
