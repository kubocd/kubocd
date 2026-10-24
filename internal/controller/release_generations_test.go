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
	"crypto/sha256"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestAReadyConditionOfAnOlderSpecIsStale(t *testing.T) {
	ready := func(status metav1.ConditionStatus, generation int64) []metav1.Condition {
		return []metav1.Condition{{Type: "Ready", Status: status, ObservedGeneration: generation}}
	}
	cases := map[string]struct {
		conditions           []metav1.Condition
		observed, generation int64
		stale                bool
	}{
		"missing":                                 {nil, 1, 2, false},
		"of an older generation":                  {ready(metav1.ConditionTrue, 1), 1, 2, true},
		"False of an older generation":            {ready(metav1.ConditionFalse, 1), 1, 2, true},
		"of the generation":                       {ready(metav1.ConditionTrue, 2), 2, 2, false},
		"True, the object observed an older one":  {ready(metav1.ConditionTrue, 2), 1, 2, true},
		"False, the object observed an older one": {ready(metav1.ConditionFalse, 2), 1, 2, false},
		"of a newer generation":                   {ready(metav1.ConditionTrue, 3), 3, 2, false},
		"without generation, as by test":          {ready(metav1.ConditionTrue, 0), 0, 0, false},
	}
	for name, c := range cases {
		if got := staleReady(c.conditions, c.observed, c.generation); got != c.stale {
			t.Errorf("%s: got stale %t, want %t", name, got, c.stale)
		}
	}
}

// helmReleaseOfGeneration is the HelmRelease of the module main at the
// generation given, whose Ready condition, of the status given, and Released
// condition were set for the generation observed, which its status records.
func helmReleaseOfGeneration(generation, observed int64, ready metav1.ConditionStatus) *fluxv2.HelmRelease {
	helmRelease := readyHelmRelease()
	helmRelease.Generation = generation
	helmRelease.Status.ObservedGeneration = observed
	helmRelease.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: ready, Reason: "Succeeded", ObservedGeneration: observed, LastTransitionTime: metav1.Now()},
		{Type: "Released", Status: metav1.ConditionTrue, Reason: "InstallSucceeded", ObservedGeneration: observed,
			Message: "Helm install succeeded for release apps/demo-main.v1", LastTransitionTime: metav1.Now()},
	}
	return helmRelease
}

// connectionDb reads the output Connection db of the Release demo, and false
// when it does not exist.
func connectionDb(t *testing.T, testbed *releaseTestbed) bool {
	t.Helper()
	err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "kcd-demo-db"}, &kv1alpha1.Connection{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func TestAHelmReleaseReadyForAnOlderGenerationKeepsTheReleaseWaiting(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, databaseContract, kv1alpha1.KindConnection, ""), "")
	for _, ready := range []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse} {
		t.Run(string(ready), func(t *testing.T) {
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, helmReleaseOfGeneration(2, 1, ready)))
			got := testbed.reconcile(t)
			if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseWaitHelmReleases ||
				got.status != "HelmRelease main: Deploying..." {
				t.Fatalf("got the phase %q, the status %q and the error %v, want %q and the status of a deployment", phase,
					got.status, got.err, kv1alpha1.ReleasePhaseWaitHelmReleases)
			}
			if connectionDb(t, testbed) || slices.ContainsFunc(got.events, func(e string) bool { return strings.Contains(e, "HelmRelease:main") }) {
				t.Fatalf("got the output Connection or the events %q, want neither", got.events)
			}
		})
	}
	// Ready for its generation by its condition, while the HelmRelease records an older one
	unreconciled := helmReleaseOfGeneration(2, 2, metav1.ConditionTrue)
	unreconciled.Status.ObservedGeneration = 1
	testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, unreconciled))
	if got := testbed.reconcile(t); got.err != nil || connectionDb(t, testbed) || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseWaitHelmReleases {
		t.Fatalf("got the phase %q, the status %q and the error %v, want the Release waiting", phaseOf(t, testbed), got.status, got.err)
	}
	// A control: the same HelmRelease, ready for its generation
	testbed = newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, helmReleaseOfGeneration(2, 2, metav1.ConditionTrue)))
	if got := testbed.reconcile(t); got.err != nil || !connectionDb(t, testbed) || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseWaitOutputConnections {
		t.Fatalf("got the phase %q, the status %q and the error %v, want the output Connection created", phaseOf(t, testbed),
			got.status, got.err)
	}
}

// bumpingGenerations acts as the API server for the HelmReleases: a patch that
// changes the spec bumps the generation, which the caller gets back.
func bumpingGenerations(t *testing.T) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				helmRelease, ok := obj.(*fluxv2.HelmRelease)
				if !ok {
					return c.Patch(ctx, obj, patch, opts...)
				}
				before := &fluxv2.HelmRelease{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(obj), before); err != nil {
					return err
				}
				if err := c.Patch(ctx, obj, patch, opts...); err != nil {
					return err
				}
				if equality.Semantic.DeepEqual(before.Spec, helmRelease.Spec) {
					return nil
				}
				helmRelease.Generation = before.Generation + 1
				return c.Update(ctx, helmRelease)
			},
		})
	}
}

func TestAPatchedHelmReleaseKeepsTheReleaseWaitingUntilFluxReconcilesIt(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)), bumpingGenerations(t))
	got := testbed.reconcile(t)
	if helmRelease := getDemoMain(t, testbed); helmRelease.Generation != 2 {
		t.Fatalf("got the generation %d, want the patch to bump it", helmRelease.Generation)
	}
	if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseWaitHelmReleases {
		t.Fatalf("got the phase %q, the status %q and the error %v, want the Release waiting", phase, got.status, got.err)
	}
	// Flux reconciles the new generation
	helmRelease := getDemoMain(t, testbed)
	for i := range helmRelease.Status.Conditions {
		helmRelease.Status.Conditions[i].ObservedGeneration = 2
	}
	helmRelease.Status.ObservedGeneration = 2
	if err := testbed.client.Update(context.Background(), helmRelease); err != nil {
		t.Fatal(err)
	}
	if got := testbed.reconcile(t); got.err != nil || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseReady {
		t.Fatalf("got the phase %q, the status %q and the error %v, want READY", phaseOf(t, testbed), got.status, got.err)
	}
}

// withSourceGeneration puts the Flux source of the kind given of the Release
// demo at generation 2, its Ready condition, of the status given, set for the
// generation observed, and its status recording the generation recorded.
func withSourceGeneration(t *testing.T, kind string, observed, recorded int64, ready metav1.ConditionStatus) func(*ReleaseReconciler) {
	return func(r *ReleaseReconciler) {
		ctx := context.Background()
		key := types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(OciRepositoryNameFormat, "demo")}
		conditions := []metav1.Condition{{Type: "Ready", Status: ready, Reason: "Succeeded", Message: "of generation 1",
			ObservedGeneration: observed, LastTransitionTime: metav1.Now()}}
		var source client.Object = &sourcev1.HelmRepository{}
		if kind == "OCIRepository" {
			source = &sourcev1.OCIRepository{}
		}
		if err := r.Get(ctx, key, source); err != nil {
			t.Fatal(err)
		}
		source.SetGeneration(2)
		switch s := source.(type) {
		case *sourcev1.OCIRepository:
			s.Status.Conditions, s.Status.ObservedGeneration = conditions, recorded
		case *sourcev1.HelmRepository:
			s.Status.Conditions, s.Status.ObservedGeneration = conditions, recorded
		}
		if err := r.Update(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
}

// countedArchive serves the package of the Release demo, as servedPackage
// does, and counts the requests.
func countedArchive(t *testing.T, requests *atomic.Int32) func(*ReleaseReconciler) {
	t.Helper()
	files := map[string]string{"original.yaml": fmt.Sprintf(afterRenderPackage, "", ""),
		"status.yaml": "apiVersion: v1alpha1\nchartByModule:\n  main: {name: demo, version: 1.0.0}\n"}
	archive := packageArchive(t, files)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	return func(r *ReleaseReconciler) {
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

// countingGets counts the reads of the package cache.
type countingGets struct {
	cache.Cache
	gets atomic.Int32
}

func (c *countingGets) Get(key string) cache.Entry {
	c.gets.Add(1)
	return c.Cache.Get(key)
}

func TestAFluxSourceReadyForAnOlderGenerationIsWaitedFor(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "")
	cases := map[string]struct {
		ready              metav1.ConditionStatus
		observed, recorded int64
	}{
		"True of an older generation":     {metav1.ConditionTrue, 1, 1},
		"False of an older generation":    {metav1.ConditionFalse, 1, 1},
		"True, the source recorded older": {metav1.ConditionTrue, 2, 1},
	}
	for name, c := range cases {
		t.Run("OCIRepository, "+name, func(t *testing.T) {
			var requests atomic.Int32
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, coldFolder(t), countedArchive(t, &requests),
				withSourceGeneration(t, "OCIRepository", c.observed, c.recorded, c.ready))
			got := testbed.reconcile(t)
			if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseWaitOci || got.status != waitOciRepository ||
				requests.Load() != 0 {
				t.Fatalf("got the phase %q, the status %q, the error %v and %d requests of the artifact, want the wait and none",
					phase, got.status, got.err, requests.Load())
			}
		})
		t.Run("HelmRepository, "+name, func(t *testing.T) {
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext,
				withSourceGeneration(t, "HelmRepository", c.observed, c.recorded, c.ready))
			cached := &countingGets{Cache: testbed.reconciler.PackageCache}
			testbed.reconciler.PackageCache = cached
			got := testbed.reconcile(t)
			if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseWaitHelmRepo || got.status != waitHelmRepository ||
				cached.gets.Load() != 0 {
				t.Fatalf("got the phase %q, the status %q, the error %v and %d reads of the cache, want the wait and none", phase,
					got.status, got.err, cached.gets.Load())
			}
			err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "demo-main"}, &fluxv2.HelmRelease{})
			if !apierrors.IsNotFound(err) {
				t.Fatalf("got the HelmRelease (%v), want none", err)
			}
		})
	}
	// Controls: the same sources, ready for their generation
	var requests atomic.Int32
	oci := newReleaseTestbed(t, manifest, "", afterRenderContext, coldFolder(t), countedArchive(t, &requests),
		func(r *ReleaseReconciler) { r.Fetcher = NewPackageFetcher(DefaultPackageLimits, 0, "") },
		withSourceGeneration(t, "OCIRepository", 2, 2, metav1.ConditionTrue))
	if got := oci.reconcile(t); got.err != nil || requests.Load() != 1 || phaseOf(t, oci) == kv1alpha1.ReleasePhaseWaitOci {
		t.Fatalf("got the phase %q, the status %q, the error %v and %d requests, want the artifact fetched", phaseOf(t, oci),
			got.status, got.err, requests.Load())
	}
	helmRepository := newReleaseTestbed(t, manifest, "", afterRenderContext, withSourceGeneration(t, "HelmRepository", 2, 2, metav1.ConditionTrue))
	if got := helmRepository.reconcile(t); got.err != nil || phaseOf(t, helmRepository) == kv1alpha1.ReleasePhaseWaitHelmRepo {
		t.Fatalf("got the phase %q, the status %q and the error %v, want the Release past its HelmRepository", phaseOf(t, helmRepository),
			got.status, got.err)
	}
}
