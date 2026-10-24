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
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// installedV1 is the Released condition of the install of generation 1.
func installedV1() metav1.Condition {
	return metav1.Condition{Type: "Released", Status: metav1.ConditionTrue, Reason: "InstallSucceeded", ObservedGeneration: 1,
		Message: "Helm install succeeded for release apps/demo-main.v1 with chart demo@1.0.0", LastTransitionTime: metav1.Now()}
}

// upgradeRunning is the Ready condition that helm-controller sets before it
// runs the upgrade of generation 2.
func upgradeRunning() metav1.Condition {
	return metav1.Condition{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", ObservedGeneration: 2,
		Message: "Running 'upgrade' action with timeout of 5m0s", LastTransitionTime: metav1.Now()}
}

func TestTheStateOfAHelmReleaseHasNoReleasedMessageWhileReadyIsUnknown(t *testing.T) {
	upgradeFailed := metav1.Condition{Type: "Released", Status: metav1.ConditionFalse, Reason: "UpgradeFailed", ObservedGeneration: 2,
		Message: "Helm upgrade failed for release apps/demo-main with chart demo@1.0.0", LastTransitionTime: metav1.Now()}
	cases := map[string]struct {
		conditions []metav1.Condition
		want       kv1alpha1.HelmReleaseState
	}{
		"Unknown before an upgrade": {[]metav1.Condition{upgradeRunning(), installedV1()},
			kv1alpha1.HelmReleaseState{Ready: metav1.ConditionUnknown}},
		"Unknown before an upgrade, Released first": {[]metav1.Condition{installedV1(), upgradeRunning()},
			kv1alpha1.HelmReleaseState{Ready: metav1.ConditionUnknown}},
		"Unknown while the tests of the upgrade run": {[]metav1.Condition{{Type: "Ready", Status: metav1.ConditionUnknown,
			Reason: "AwaitingTests", ObservedGeneration: 2, LastTransitionTime: metav1.Now(),
			Message: "Helm release apps/demo-main.v2 with chart demo@1.0.0 is awaiting tests"},
			{Type: "Released", Status: metav1.ConditionTrue, Reason: "UpgradeSucceeded", ObservedGeneration: 2,
				Message: "Helm upgrade succeeded for release apps/demo-main.v2 with chart demo@1.0.0", LastTransitionTime: metav1.Now()}},
			kv1alpha1.HelmReleaseState{Ready: metav1.ConditionUnknown}},
		// helm-controller summarizes Released of the previous generation when the new one needs no Helm action
		"True from the release of the previous generation": {[]metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue,
			Reason: "InstallSucceeded", ObservedGeneration: 2, Message: installedV1().Message, LastTransitionTime: metav1.Now()},
			installedV1()}, kv1alpha1.HelmReleaseState{Ready: metav1.ConditionTrue, Status: installedV1().Message}},
		"False after a failed upgrade": {[]metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "UpgradeFailed",
			ObservedGeneration: 2, Message: upgradeFailed.Message, LastTransitionTime: metav1.Now()}, upgradeFailed},
			kv1alpha1.HelmReleaseState{Ready: metav1.ConditionFalse, Status: upgradeFailed.Message}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			helmRelease := readyHelmRelease()
			helmRelease.Generation = 2
			helmRelease.Status.ObservedGeneration = 2
			helmRelease.Status.Conditions = c.conditions
			if got := computeHelmReleaseState(helmRelease); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestAHelmReleaseThatStartsAnUpgradeGivesNoWarningWithThePreviousMessage(t *testing.T) {
	testbed := newReleaseTestbed(t, fmt.Sprintf(afterRenderPackage, "", ""), "", afterRenderContext,
		withObjects(t, helmReleaseOfGeneration(1, 1, metav1.ConditionTrue)))
	if got := testbed.reconcile(t); got.err != nil || phaseOf(t, testbed) != kv1alpha1.ReleasePhaseReady {
		t.Fatalf("got the phase %q, the status %q and the error %v, want READY", phaseOf(t, testbed), got.status, got.err)
	}
	// helm-controller starts the upgrade of generation 2
	helmRelease := getDemoMain(t, testbed)
	helmRelease.Generation = 2
	helmRelease.Status.Conditions = []metav1.Condition{upgradeRunning(), installedV1()}
	if err := testbed.client.Update(context.Background(), helmRelease); err != nil {
		t.Fatal(err)
	}
	got := testbed.reconcile(t)
	if phase := phaseOf(t, testbed); got.err != nil || phase != kv1alpha1.ReleasePhaseWaitHelmReleases ||
		got.status != "HelmRelease main: Deploying..." {
		t.Fatalf("got the phase %q, the status %q and the error %v, want %q and the status of a deployment", phase, got.status,
			got.err, kv1alpha1.ReleasePhaseWaitHelmReleases)
	}
	if slices.ContainsFunc(got.events, func(e string) bool { return strings.Contains(e, "HelmRelease:main") }) {
		t.Fatalf("got the events %q, want none about the HelmRelease", got.events)
	}
}
