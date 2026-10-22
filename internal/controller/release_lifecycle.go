/*
Copyright 2026 CGI France

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
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"reflect"
	"sort"
	"strings"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const noNameModule = "noname"

// SkipDeletionWaitAnnotation set to "true" on a Release lets its deletion complete without waiting for its HelmReleases.
const SkipDeletionWaitAnnotation = "kubocd.kubotal.io/skip-deletion-wait"

// listOwnedHelmReleases returns the HelmReleases controlled by the release, sorted by name.
func (r *ReleaseReconciler) listOwnedHelmReleases(ctx context.Context, release *kv1alpha1.Release) ([]fluxv2.HelmRelease, error) {
	list := &fluxv2.HelmReleaseList{}
	if err := r.List(ctx, list, client.InNamespace(release.Namespace)); err != nil {
		return nil, fmt.Errorf("unable to list HelmReleases: %w", err)
	}
	owned := make([]fluxv2.HelmRelease, 0, len(list.Items))
	for idx := range list.Items {
		if metav1.IsControlledBy(&list.Items[idx], release) {
			owned = append(owned, list.Items[idx])
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

// removeHelmReleases deletes the given HelmReleases, dependents first: one is deleted only once none of the
// others depends on it. It returns the one to report as pending, or nil when none is left.
func (r *ReleaseReconciler) removeHelmReleases(ctx context.Context, helmReleases []fluxv2.HelmRelease) (*fluxv2.HelmRelease, error) {
	if len(helmReleases) == 0 {
		return nil, nil
	}
	dependedOn := make(map[string]bool)
	for _, hr := range helmReleases {
		for _, dep := range hr.Spec.DependsOn {
			if dep.Name != hr.Name && (dep.Namespace == "" || dep.Namespace == hr.Namespace) {
				dependedOn[dep.Name] = true
			}
		}
	}
	leaves := make([]fluxv2.HelmRelease, 0, len(helmReleases))
	for _, hr := range helmReleases {
		if !dependedOn[hr.Name] {
			leaves = append(leaves, hr)
		}
	}
	if len(leaves) == 0 {
		// Dependency cycle. Do not block forever
		leaves = helmReleases
	}
	for idx := range leaves {
		if leaves[idx].DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &leaves[idx]); client.IgnoreNotFound(err) != nil {
				return nil, fmt.Errorf("unable to delete HelmRelease '%s': %w", leaves[idx].Name, err)
			}
		}
	}
	for idx := range leaves {
		if computeHelmReleaseState(&leaves[idx]).Ready == metav1.ConditionFalse {
			return &leaves[idx], nil
		}
	}
	return &leaves[0], nil
}

// uninstallStatus is the Flux message of a HelmRelease being removed
func uninstallStatus(helmRelease *fluxv2.HelmRelease) string {
	for _, condition := range helmRelease.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse && condition.Message != "" {
			return condition.Message
		}
	}
	return "uninstalling"
}

// deleteOwnedHelmReleases removes the HelmReleases of a Release under deletion. done is true once none is left.
func (r *ReleaseReconciler) deleteOwnedHelmReleases(op *releaseOperation) (result ctrl.Result, done bool, err error) {
	owned, err := r.listOwnedHelmReleases(op.ctx, op.release)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	pending, err := r.removeHelmReleases(op.ctx, owned)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	if pending == nil {
		return ctrl.Result{}, true, nil
	}
	states := make(map[string]kv1alpha1.HelmReleaseState)
	for idx := range owned {
		states[moduleNameFromHelmRelease(op.release.Name, owned[idx].Name)] = computeHelmReleaseState(&owned[idx])
	}
	force := !reflect.DeepEqual(states, op.release.Status.HelmReleaseStates)
	op.release.Status.HelmReleaseStates = states
	message := fmt.Sprintf("HelmRelease %s: %s", pending.Name, uninstallStatus(pending))
	if message != op.release.Status.Message {
		r.Event(op.release, "Normal", "Deleting", message)
	}
	result, err = r.updateStatus(op, kv1alpha1.ReleasePhaseDeleting, message, force)
	return result, false, err
}

// pruneHelmReleases removes the HelmReleases of the release whose module is no longer deployed.
// It returns a message while some are still present.
func (r *ReleaseReconciler) pruneHelmReleases(op *releaseOperation, wanted map[string]bool) (string, ReconcileError) {
	owned, err := r.listOwnedHelmReleases(op.ctx, op.release)
	if err != nil {
		return "", NewReconcileError(err, false, "HelmReleaseAccess")
	}
	unwanted := make([]fluxv2.HelmRelease, 0)
	for _, hr := range owned {
		if !wanted[hr.Name] {
			unwanted = append(unwanted, hr)
		}
	}
	pending, err := r.removeHelmReleases(op.ctx, unwanted)
	if err != nil {
		return "", NewReconcileError(err, false, "HelmReleaseDelete")
	}
	if pending == nil {
		return "", nil
	}
	return fmt.Sprintf("HelmRelease %s (being removed): %s", pending.Name, uninstallStatus(pending)), nil
}

// moduleNameFromHelmRelease is the reverse of BuildHelmReleaseName
func moduleNameFromHelmRelease(releaseName, helmReleaseName string) string {
	if helmReleaseName == releaseName {
		return noNameModule
	}
	return strings.TrimPrefix(helmReleaseName, releaseName+"-")
}
