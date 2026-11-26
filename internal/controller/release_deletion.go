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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"slices"
	"strings"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// DeletionRequeueDelay is the delay between two passes of the deletion of a Release that waits for its children.
const DeletionRequeueDelay = 5 * time.Second

// reader reads around the cache: a child just created or changed may not be in the informer cache yet.
func (r *ReleaseReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// reconcileDelete runs one pass of the deletion of a Release that holds the finalizer of KuboCD, in this order:
//   - the child HelmReleases, the dependent ones before the ones they depend on, while the Flux sources and the
//     chart folder they install from stay in place.
//   - once they are gone, the Claims, output Connections and ClusterConnections, HelmRepository and OCIRepository of
//     the Release.
//   - the chart folder, then the finalizer.
//
// A pass that waits for a child to go writes the DELETING status once and requeues the Release. A failing pass writes
// the error in the DELETING status, emits a Warning event, and returns the error for retry. Once no child remains, the
// finalizer is removed directly without an extra status write.
func (r *ReleaseReconciler) reconcileDelete(op *releaseOperation, helmRepositoryFolder string) (ctrl.Result, error) {
	op.logger.V(0).Info("Deleting release")

	// ------------------------------------------------------------ Step 1: the HelmReleases
	helmReleases, err := r.deleteChildHelmReleases(op)
	if err != nil {
		return r.failDeletion(op, err)
	}
	if len(helmReleases) > 0 {
		return r.waitDeletion(op, fmt.Sprintf("Waiting for the deletion of the HelmReleases %s", strings.Join(helmReleases, ", ")))
	}

	// ------------------------------------------------------------ Step 2: the other children
	children, err := r.deleteOtherChildren(op)
	if err != nil {
		return r.failDeletion(op, err)
	}
	if len(children) > 0 {
		return r.waitDeletion(op, fmt.Sprintf("Waiting for the deletion of %s", strings.Join(children, ", ")))
	}

	if err := misc.SafeRemove(helmRepositoryFolder); err != nil {
		op.logger.Error(err, "Failed to remove helm repository folder", "folder", helmRepositoryFolder)
	}
	controllerutil.RemoveFinalizer(op.release, global.FinalizerName)
	op.logger.V(1).Info(">-> Update resource (Remove finalizer)")
	if err := r.Update(op.ctx, op.release); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("could not remove the finalizer of the Release: %s", apiError(err))
	}
	r.statusFailures.clear(op.request.NamespacedName)
	return ctrl.Result{}, nil
}

// failDeletion writes the error in the DELETING status, emits a Warning event, and returns err for retry.
func (r *ReleaseReconciler) failDeletion(op *releaseOperation, err error) (ctrl.Result, error) {
	r.publishDeleting(op, err.Error())
	if r.EventRecorder != nil {
		r.Event(op.release, "Warning", "DeletionError", err.Error())
	}
	return ctrl.Result{}, err
}

// waitDeletion writes the message in the status of the Release, and requeues it.
func (r *ReleaseReconciler) waitDeletion(op *releaseOperation, message string) (ctrl.Result, error) {
	r.publishDeleting(op, message)
	return ctrl.Result{RequeueAfter: DeletionRequeueDelay}, nil
}

// publishDeleting writes the phase DELETING and the message in the status of the Release. A status that cannot be
// written does not stop the deletion.
func (r *ReleaseReconciler) publishDeleting(op *releaseOperation, message string) {
	if _, err := r.updateStatus(op, kv1alpha1.ReleasePhaseDeleting, message, false); err != nil {
		op.logger.Error(err, "Deleting the Release without its status")
	}
}

// absentKind tells whether err says the kind is not served or not known to the client: no child of that kind
// can exist.
func absentKind(err error) bool {
	return apimeta.IsNoMatchError(err) || runtime.IsNotRegisteredError(err)
}

// deleteChildHelmReleases deletes the HelmReleases the Release controls that no other one still present depends on,
// and returns the names of those still present. HelmReleases that depend on each other in a cycle are deleted
// together, once no HelmRelease out of the cycle depends on one of them. While a HelmRelease under uninstallation
// depends on one that is gone, no other one is deleted.
func (r *ReleaseReconciler) deleteChildHelmReleases(op *releaseOperation) ([]string, error) {
	list := &fluxv2.HelmReleaseList{}
	if err := r.reader().List(op.ctx, list, client.InNamespace(op.release.Namespace)); absentKind(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("could not list the HelmReleases of the Release: %s", apiError(err))
	}
	children := make([]*fluxv2.HelmRelease, 0, len(list.Items))
	present := make(map[string]bool)
	for i := range list.Items {
		if metav1.IsControlledBy(&list.Items[i], op.release) {
			children = append(children, &list.Items[i])
			present[list.Items[i].Name] = true
		}
	}
	if len(children) == 0 {
		return nil, nil
	}
	dependencies := make(map[string][]string)
	// A HelmRelease under uninstallation whose dependency is gone may still need what that dependency depended on
	waitAll := false
	for _, child := range children {
		for _, dependency := range child.Spec.DependsOn {
			namespace := misc.Ternary(dependency.Namespace == "", child.Namespace, dependency.Namespace)
			if namespace != op.release.Namespace || dependency.Name == child.Name {
				continue
			}
			if present[dependency.Name] {
				dependencies[child.Name] = append(dependencies[child.Name], dependency.Name)
			} else if !child.DeletionTimestamp.IsZero() {
				waitAll = true
			}
		}
	}
	blocked := blockedHelmReleases(dependencies)
	names := make([]string, 0, len(children))
	var failed error
	for _, child := range children {
		names = append(names, child.Name)
		if !child.DeletionTimestamp.IsZero() || blocked[child.Name] || waitAll {
			continue
		}
		op.logger.V(0).Info("Deleting HelmRelease", "name", child.Name)
		if err := client.IgnoreNotFound(r.Delete(op.ctx, child, client.Preconditions{UID: &child.UID})); err != nil {
			op.logger.Error(errors.New(apiError(err)), "Failed to delete HelmRelease", "helmRelease", child.Name)
			if failed == nil {
				failed = fmt.Errorf("could not delete the HelmRelease '%s': %s", child.Name, apiError(err))
			}
		}
	}
	slices.Sort(names)
	return names, failed
}

// blockedHelmReleases returns the HelmReleases that must wait: one that a HelmRelease out of its cycle depends on.
// dependencies gives, for each HelmRelease, the HelmReleases it depends on.
func blockedHelmReleases(dependencies map[string][]string) map[string]bool {
	reachable := func(from string) map[string]bool {
		seen := map[string]bool{}
		stack := []string{from}
		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, next := range dependencies[current] {
				if !seen[next] {
					seen[next] = true
					stack = append(stack, next)
				}
			}
		}
		return seen
	}
	reach := make(map[string]map[string]bool, len(dependencies))
	for name := range dependencies {
		reach[name] = reachable(name)
	}
	sameCycle := func(a, b string) bool { return reach[a][b] && reach[b][a] }
	blocked := make(map[string]bool)
	for dependent, targets := range dependencies {
		for _, target := range targets {
			if !sameCycle(dependent, target) {
				blocked[target] = true
				// A cycle goes as a whole: each HelmRelease of the cycle of target waits too
				for member := range reach[target] {
					if sameCycle(member, target) {
						blocked[member] = true
					}
				}
			}
		}
	}
	return blocked
}

// isOutputClusterConnectionOf reports whether clusterConnection lists release as its parent Release.
func isOutputClusterConnectionOf(clusterConnection *kv1alpha1.ClusterConnection, release *kv1alpha1.Release) bool {
	parent := clusterConnection.Spec.ParentRelease
	return parent != nil && parent.Namespace == release.Namespace && parent.Name == release.Name
}

// outputClusterConnections lists around the cache the ClusterConnections whose parent is the Release.
func (r *ReleaseReconciler) outputClusterConnections(op *releaseOperation) ([]kv1alpha1.ClusterConnection, error) {
	list := &kv1alpha1.ClusterConnectionList{}
	if err := r.reader().List(op.ctx, list); err != nil {
		return nil, fmt.Errorf("could not list the ClusterConnections of the Release: %s", apiError(err))
	}
	clusterConnections := make([]kv1alpha1.ClusterConnection, 0)
	for i := range list.Items {
		if isOutputClusterConnectionOf(&list.Items[i], op.release) {
			clusterConnections = append(clusterConnections, list.Items[i])
		}
	}
	return clusterConnections, nil
}

// deleteOutputClusterConnections lists and deletes the ClusterConnections that the Release created, and returns the
// matched ClusterConnections and the first error once each deletion is tried.
func (r *ReleaseReconciler) deleteOutputClusterConnections(op *releaseOperation) ([]kv1alpha1.ClusterConnection, error) {
	clusterConnections, err := r.outputClusterConnections(op)
	if err != nil {
		return nil, err
	}
	var failed error
	for i := range clusterConnections {
		clusterConnection := &clusterConnections[i]
		if !clusterConnection.DeletionTimestamp.IsZero() {
			continue
		}
		err := r.Delete(op.ctx, clusterConnection, client.Preconditions{UID: &clusterConnection.UID})
		if err := client.IgnoreNotFound(err); err != nil {
			op.logger.Error(errors.New(apiError(err)), "Failed to delete ClusterConnection", "clusterConnection", clusterConnection.Name)
			if failed == nil {
				failed = fmt.Errorf("could not delete the ClusterConnections of the Release: %s", apiError(err))
			}
		}
	}
	return clusterConnections, failed
}

// deleteOtherChildren deletes the Claims, output Connections and ClusterConnections, HelmRepository and
// OCIRepository of the Release, and returns those still present, as '<Kind> <name>'.
func (r *ReleaseReconciler) deleteOtherChildren(op *releaseOperation) ([]string, error) {
	clusterConnections, err := r.deleteOutputClusterConnections(op)
	if err != nil {
		return nil, err
	}
	claims := &kv1alpha1.ClaimList{}
	if err := r.reader().List(op.ctx, claims, client.InNamespace(op.release.Namespace)); err != nil {
		return nil, fmt.Errorf("could not list the Claims of the Release: %s", apiError(err))
	}
	connections := &kv1alpha1.ConnectionList{}
	if err := r.reader().List(op.ctx, connections, client.InNamespace(op.release.Namespace)); err != nil {
		return nil, fmt.Errorf("could not list the Connections of the Release: %s", apiError(err))
	}
	children := make([]client.Object, 0, len(claims.Items)+len(connections.Items)+2)
	for i := range claims.Items {
		children = append(children, &claims.Items[i])
	}
	for i := range connections.Items {
		children = append(children, &connections.Items[i])
	}
	for _, source := range []client.Object{&sourcev1.HelmRepository{}, &sourcev1.OCIRepository{}} {
		name := op.helmRepositoryName
		if _, ok := source.(*sourcev1.OCIRepository); ok {
			name = op.ociRepositoryName
		}
		err := r.reader().Get(op.ctx, types.NamespacedName{Namespace: op.release.Namespace, Name: name}, source)
		if err == nil {
			children = append(children, source)
		} else if !apierrors.IsNotFound(err) && !absentKind(err) {
			return nil, fmt.Errorf("could not get the %s '%s' of the Release: %s", childKind(source), name, apiError(err))
		}
	}
	var failed error
	for _, child := range children {
		if !metav1.IsControlledBy(child, op.release) || !child.GetDeletionTimestamp().IsZero() {
			continue
		}
		uid := child.GetUID()
		op.logger.V(0).Info("Deleting child", "kind", childKind(child), "name", child.GetName())
		if err := client.IgnoreNotFound(r.Delete(op.ctx, child, client.Preconditions{UID: &uid})); err != nil {
			op.logger.Error(errors.New(apiError(err)), "Failed to delete child", "kind", childKind(child), "name", child.GetName())
			if failed == nil {
				failed = fmt.Errorf("could not delete the %s '%s' of the Release: %s", childKind(child), child.GetName(), apiError(err))
			}
		}
	}
	if failed != nil {
		return nil, failed
	}
	return r.presentChildren(op, children, clusterConnections)
}

// presentChildren returns, as '<Kind> <name>', the children the Release controls that are still present, including
// its ClusterConnections. Each Get uses a fresh empty object so absent JSON fields cannot leave stale metadata.
func (r *ReleaseReconciler) presentChildren(op *releaseOperation, children []client.Object,
	clusterConnections []kv1alpha1.ClusterConnection) ([]string, error) {
	present := make([]string, 0)
	for _, child := range children {
		if !metav1.IsControlledBy(child, op.release) {
			continue
		}
		fresh := newEmptyObject(child)
		err := r.reader().Get(op.ctx, client.ObjectKeyFromObject(child), fresh)
		if err == nil && metav1.IsControlledBy(fresh, op.release) {
			present = append(present, fmt.Sprintf("%s %s", childKind(fresh), fresh.GetName()))
		} else if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get the %s '%s' of the Release: %s", childKind(child), child.GetName(), apiError(err))
		}
	}
	for i := range clusterConnections {
		clusterConnection := &clusterConnections[i]
		fresh := &kv1alpha1.ClusterConnection{}
		err := r.reader().Get(op.ctx, client.ObjectKeyFromObject(clusterConnection), fresh)
		if err == nil && isOutputClusterConnectionOf(fresh, op.release) {
			present = append(present, fmt.Sprintf("%s %s", kv1alpha1.KindClusterConnection, fresh.Name))
		} else if err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get the %s '%s' of the Release: %s", kv1alpha1.KindClusterConnection, clusterConnection.Name, apiError(err))
		}
	}
	slices.Sort(present)
	return present, nil
}

// newEmptyObject returns a fresh zero-valued object of the same concrete type as object.
func newEmptyObject(object client.Object) client.Object {
	switch object.(type) {
	case *fluxv2.HelmRelease:
		return &fluxv2.HelmRelease{}
	case *kv1alpha1.Claim:
		return &kv1alpha1.Claim{}
	case *kv1alpha1.Connection:
		return &kv1alpha1.Connection{}
	case *kv1alpha1.ClusterConnection:
		return &kv1alpha1.ClusterConnection{}
	case *sourcev1.HelmRepository:
		return &sourcev1.HelmRepository{}
	case *sourcev1.OCIRepository:
		return &sourcev1.OCIRepository{}
	default:
		panic(fmt.Sprintf("unexpected child type %T", object))
	}
}

// childKind is the kind of a child of a Release, for messages.
func childKind(object client.Object) string {
	switch object.(type) {
	case *kv1alpha1.Claim:
		return "Claim"
	case *kv1alpha1.Connection:
		return "Connection"
	case *kv1alpha1.ClusterConnection:
		return string(kv1alpha1.KindClusterConnection)
	case *sourcev1.HelmRepository:
		return sourcev1.HelmRepositoryKind
	case *sourcev1.OCIRepository:
		return sourcev1.OCIRepositoryKind
	default:
		return fmt.Sprintf("%T", object)
	}
}
