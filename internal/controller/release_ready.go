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

package controller

import (
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
	"unicode/utf8"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readyReasons is the reason of the Ready condition of a Release in each phase.
var readyReasons = map[kv1alpha1.ReleasePhase]string{
	kv1alpha1.ReleasePhaseReady:                 "Succeeded",
	kv1alpha1.ReleasePhaseError:                 "Failed",
	kv1alpha1.ReleasePhaseWaitOci:               "WaitingForOCIRepository",
	kv1alpha1.ReleasePhaseWaitHelmRepo:          "WaitingForHelmRepository",
	kv1alpha1.ReleasePhaseWaitHelmReleases:      "WaitingForHelmReleases",
	kv1alpha1.ReleasePhaseWaitOutputConnections: "WaitingForOutputConnections",
	kv1alpha1.ReleasePhaseWaitDependencies:      "WaitingForDependencies",
	kv1alpha1.ReleasePhaseWaitInputConnections:  "WaitingForInputConnections",
	kv1alpha1.ReleasePhaseWaitClaims:            "WaitingForClaims",
	kv1alpha1.ReleasePhaseSuspended:             "Suspended",
	kv1alpha1.ReleasePhaseWaitConditions:        "WaitingForConditions",
	kv1alpha1.ReleasePhaseDeleting:              "Deleting",
}

// readyMessage is the message of a Ready condition that is True.
const readyMessage = "The Release is ready"

// maxConditionMessage is the maximum length of the message of a metav1.Condition.
const maxConditionMessage = 32768

// readyCondition is the Ready condition of a Release of the generation given, in the phase given,
// with the status message given. It is True in the phase READY only.
func readyCondition(phase kv1alpha1.ReleasePhase, message string, generation int64) metav1.Condition {
	condition := metav1.Condition{
		Type:               kv1alpha1.ReleaseConditionReady,
		Status:             metav1.ConditionFalse,
		ObservedGeneration: generation,
		Reason:             readyReasons[phase],
		Message:            truncateMessage(message),
	}
	if condition.Reason == "" {
		condition.Reason = "Progressing"
	}
	if phase == kv1alpha1.ReleasePhaseReady {
		condition.Status = metav1.ConditionTrue
		condition.Message = readyMessage
	}
	return condition
}

// readyConditionUpToDate tells whether the conditions hold the Ready condition of readyCondition.
func readyConditionUpToDate(conditions []metav1.Condition, phase kv1alpha1.ReleasePhase, message string, generation int64) bool {
	current := apimeta.FindStatusCondition(conditions, kv1alpha1.ReleaseConditionReady)
	if current == nil {
		return false
	}
	want := readyCondition(phase, message, generation)
	return current.Status == want.Status && current.Reason == want.Reason && current.Message == want.Message &&
		current.ObservedGeneration == want.ObservedGeneration
}

// truncateMessage cuts the message to the length a condition accepts, on a rune boundary.
func truncateMessage(message string) string {
	if len(message) <= maxConditionMessage {
		return message
	}
	const ellipsis = "..."
	cut := maxConditionMessage - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return strings.ToValidUTF8(message[:cut], "") + ellipsis
}
