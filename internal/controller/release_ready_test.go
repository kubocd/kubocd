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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"regexp"
	"strings"
	"testing"
	"time"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// conditionReason is the pattern of the reason of a metav1.Condition.
var conditionReason = regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)

func TestTheReadyConditionIsTrueInThePhaseReadyOnly(t *testing.T) {
	for phase := range readyReasons {
		condition := readyCondition(phase, "waiting", 4)
		want := metav1.ConditionFalse
		if phase == kv1alpha1.ReleasePhaseReady {
			want = metav1.ConditionTrue
		}
		if condition.Status != want || condition.ObservedGeneration != 4 || !conditionReason.MatchString(condition.Reason) {
			t.Errorf("%s: got %+v, want the status %s, the generation 4 and a valid reason", phase, condition, want)
		}
	}
	long := strings.Repeat("é", maxConditionMessage)
	if message := readyCondition(kv1alpha1.ReleasePhaseError, long, 1).Message; len(message) > maxConditionMessage ||
		!strings.HasSuffix(message, "...") || !strings.HasPrefix(message, "éé") || strings.ContainsRune(message, '�') {
		t.Errorf("got a message of %d bytes, want it cut to %d on a rune boundary", len(message), maxConditionMessage)
	}
}

// releaseState is the status of the Release demo that a client reads.
type releaseState struct {
	generation, observed int64
	phase                kv1alpha1.ReleasePhase
	ready                *metav1.Condition
}

func stateOf(t *testing.T, testbed *releaseTestbed) releaseState {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), demoKey, release); err != nil {
		t.Fatal(err)
	}
	return releaseState{generation: release.Generation, observed: release.Status.ObservedGeneration, phase: release.Status.Phase,
		ready: apimeta.FindStatusCondition(release.Status.Conditions, kv1alpha1.ReleaseConditionReady)}
}

// readyForItsGeneration tells whether a client that follows the kstatus conventions reads the Release as ready.
func (s releaseState) readyForItsGeneration() bool {
	return s.observed == s.generation && s.ready != nil && s.ready.Status == metav1.ConditionTrue && s.ready.ObservedGeneration == s.generation
}

func (s releaseState) String() string {
	return fmt.Sprintf("generation %d, observed %d, phase %s, Ready %+v", s.generation, s.observed, s.phase, s.ready)
}

// changeRelease changes the spec of the Release demo, as the API server does: the generation is bumped.
func changeRelease(t *testing.T, testbed *releaseTestbed, change func(*kv1alpha1.Release)) {
	t.Helper()
	release := &kv1alpha1.Release{}
	if err := testbed.client.Get(context.Background(), demoKey, release); err != nil {
		t.Fatal(err)
	}
	change(release)
	release.Generation++
	if err := testbed.client.Update(context.Background(), release); err != nil {
		t.Fatal(err)
	}
}

// fluxReconciles marks the HelmRelease demo-main as ready for its current generation, as Flux does.
func fluxReconciles(t *testing.T, testbed *releaseTestbed) {
	t.Helper()
	helmRelease := getDemoMain(t, testbed)
	for i := range helmRelease.Status.Conditions {
		helmRelease.Status.Conditions[i].ObservedGeneration = helmRelease.Generation
	}
	helmRelease.Status.ObservedGeneration = helmRelease.Generation
	if err := testbed.client.Update(context.Background(), helmRelease); err != nil {
		t.Fatal(err)
	}
}

// reconcileUntilReady reconciles the Release demo, Flux reconciling its HelmRelease, until it is READY.
func reconcileUntilReady(t *testing.T, testbed *releaseTestbed) {
	t.Helper()
	for range 3 {
		if got := testbed.reconcile(t); got.err != nil {
			t.Fatalf("got the error %v", got.err)
		}
		if stateOf(t, testbed).phase == kv1alpha1.ReleasePhaseReady {
			return
		}
		fluxReconciles(t, testbed)
	}
	t.Fatalf("got %s, want READY", stateOf(t, testbed))
}

// mustWaitThenBeReady checks that the reconciliation of a change leaves the Release not ready until Flux
// reconciles the patched HelmRelease, then ready for the generation given.
func mustWaitThenBeReady(t *testing.T, testbed *releaseTestbed, generation int64) {
	t.Helper()
	before := getDemoMain(t, testbed).Generation
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got the error %v", got.err)
	}
	state := stateOf(t, testbed)
	if getDemoMain(t, testbed).Generation == before {
		t.Fatalf("got the HelmRelease unchanged, want it patched for the change")
	}
	if state.readyForItsGeneration() || state.ready == nil || state.ready.Status != metav1.ConditionFalse ||
		state.ready.Reason != "WaitingForHelmReleases" || state.observed != generation || state.ready.ObservedGeneration != generation {
		t.Fatalf("got %s, want Ready False, waiting for the HelmReleases, for the generation %d", state, generation)
	}
	fluxReconciles(t, testbed)
	if got := testbed.reconcile(t); got.err != nil {
		t.Fatalf("got the error %v", got.err)
	}
	if state := stateOf(t, testbed); !state.readyForItsGeneration() || state.observed != generation {
		t.Fatalf("got %s, want Ready True for the generation %d", state, generation)
	}
}

func TestTheStatusRecordsTheGenerationItWasWrittenFor(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", "",
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)), bumpingGenerations(t))
	changeRelease(t, testbed, func(*kv1alpha1.Release) {})
	testbed.reconcile(t)
	if state := stateOf(t, testbed); state.phase != kv1alpha1.ReleasePhaseWaitHelmReleases || state.observed != 1 ||
		state.ready == nil || state.ready.ObservedGeneration != 1 || state.ready.Status != metav1.ConditionFalse {
		t.Fatalf("got %s, want the waiting phase written for the generation 1", state)
	}
	reconcileUntilReady(t, testbed)
	if state := stateOf(t, testbed); !state.readyForItsGeneration() || state.ready.Message != readyMessage {
		t.Fatalf("got %s, want Ready True for the generation 1", state)
	}
	// A change of the spec that the rendering does not see leaves the phase and the message as they are
	for generation := int64(2); generation <= 3; generation++ {
		changeRelease(t, testbed, func(*kv1alpha1.Release) {})
		if state := stateOf(t, testbed); state.readyForItsGeneration() {
			t.Fatalf("got %s, want a client to read the Release as not ready before its reconciliation", state)
		}
		if got := testbed.reconcile(t); got.err != nil {
			t.Fatal(got.err)
		}
		if state := stateOf(t, testbed); !state.readyForItsGeneration() || state.observed != generation {
			t.Fatalf("got %s, want Ready True for the generation %d", state, generation)
		}
	}
	// Nothing changed: no status update
	updates := 0
	before := stateOf(t, testbed).ready.LastTransitionTime
	testbed.reconciler.Client = countingStatusUpdates(testbed.reconciler.Client, &updates)
	testbed.reconcile(t)
	if state := stateOf(t, testbed); updates != 0 || !state.ready.LastTransitionTime.Equal(&before) {
		t.Fatalf("got %d status updates and %s, want none", updates, state)
	}
}

func TestNoStaleReadyAfterASpecChange(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", "",
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)), bumpingGenerations(t))
	changeRelease(t, testbed, func(*kv1alpha1.Release) {})
	reconcileUntilReady(t, testbed)
	changeRelease(t, testbed, func(release *kv1alpha1.Release) {
		release.Spec.ModuleOverrides = []kv1alpha1.ModuleOverrideSpec{{Module: "main", Timeout: new(kv1alpha1.NewDuration(7 * time.Minute))}}
	})
	if state := stateOf(t, testbed); state.readyForItsGeneration() || state.ready.ObservedGeneration != 1 {
		t.Fatalf("got %s, want the Ready condition of the generation 1, which a client reads as stale", state)
	}
	mustWaitThenBeReady(t, testbed, 2)
	if timeout := getDemoMain(t, testbed).Spec.Timeout; timeout == nil || timeout.Duration != 7*time.Minute {
		t.Fatalf("got the timeout %v, want the one of the change", timeout)
	}
}

func TestNoStaleReadyAfterAContextChange(t *testing.T) {
	manifest := fmt.Sprintf(afterRenderPackage, "", "    values: |\n      host: {{ .Context.db.host }}")
	testbed := newReleaseTestbed(t, manifest, "", `{"db":{"host":"pg"}}`,
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)), bumpingGenerations(t))
	changeRelease(t, testbed, func(*kv1alpha1.Release) {})
	reconcileUntilReady(t, testbed)
	kContext := &kv1alpha1.Context{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "cluster"}, kContext); err != nil {
		t.Fatal(err)
	}
	kContext.Spec.Context = &apiextensionsv1.JSON{Raw: []byte(`{"db":{"host":"pg-new"}}`)}
	if err := testbed.client.Update(context.Background(), kContext); err != nil {
		t.Fatal(err)
	}
	mustWaitThenBeReady(t, testbed, 1)
	if values := string(getDemoMain(t, testbed).Spec.Values.Raw); !strings.Contains(values, "pg-new") {
		t.Fatalf("got the values %s, want the host of the new Context", values)
	}
}

func TestNoStaleReadyAfterAnInputConnectionChange(t *testing.T) {
	testbed := newReleaseTestbed(t, connectionRefPackage, "", `{"dbName":"pg"}`,
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue), readyConnection("pg", "database", `{"host":"pg-1"}`)),
		bumpingGenerations(t))
	changeRelease(t, testbed, func(*kv1alpha1.Release) {})
	reconcileUntilReady(t, testbed)
	connection := &kv1alpha1.Connection{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "pg"}, connection); err != nil {
		t.Fatal(err)
	}
	connection.Spec.Values = &apiextensionsv1.JSON{Raw: []byte(`{"host":"pg-2"}`)}
	if err := testbed.client.Update(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	mustWaitThenBeReady(t, testbed, 1)
	if values := string(getDemoMain(t, testbed).Spec.Values.Raw); !strings.Contains(values, "pg-2") {
		t.Fatalf("got the values %s, want the host of the changed connection", values)
	}
	// The connection is no longer ready: the Release waits for it
	connection = &kv1alpha1.Connection{}
	if err := testbed.client.Get(context.Background(), types.NamespacedName{Namespace: "apps", Name: "pg"}, connection); err != nil {
		t.Fatal(err)
	}
	connection.Status.Phase = kv1alpha1.ConnectionPhaseError
	if err := testbed.client.Status().Update(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	testbed.reconcile(t)
	if state := stateOf(t, testbed); state.readyForItsGeneration() || state.ready.Reason != "WaitingForInputConnections" {
		t.Fatalf("got %s, want the Release waiting for its input connection", state)
	}
}

func TestNoStaleReadyWhenAFluxSourceMovesToANewGeneration(t *testing.T) {
	cases := map[string]struct {
		source client.Object
		reason string
	}{
		"OCIRepository":  {&sourcev1.OCIRepository{}, "WaitingForOCIRepository"},
		"HelmRepository": {&sourcev1.HelmRepository{}, "WaitingForHelmRepository"},
	}
	for kind, c := range cases {
		t.Run(kind, func(t *testing.T) {
			testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", "",
				withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)), bumpingGenerations(t))
			changeRelease(t, testbed, func(*kv1alpha1.Release) {})
			reconcileUntilReady(t, testbed)
			// The source spec changed, and Flux has not reconciled it yet
			key := types.NamespacedName{Namespace: "apps", Name: fmt.Sprintf(OciRepositoryNameFormat, "demo")}
			if err := testbed.client.Get(context.Background(), key, c.source); err != nil {
				t.Fatal(err)
			}
			c.source.SetGeneration(c.source.GetGeneration() + 1)
			if err := testbed.client.Update(context.Background(), c.source); err != nil {
				t.Fatal(err)
			}
			if got := testbed.reconcile(t); got.err != nil {
				t.Fatal(got.err)
			}
			if state := stateOf(t, testbed); state.readyForItsGeneration() || state.ready.Status != metav1.ConditionFalse ||
				state.ready.Reason != c.reason || state.ready.ObservedGeneration != 1 {
				t.Fatalf("got %s, want Ready False with the reason %s", state, c.reason)
			}
		})
	}
}

// countingStatusUpdates counts the status updates done through the client.
func countingStatusUpdates(c client.Client, updates *int) client.Client {
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			*updates++
			return c.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
}
