package controller

import (
	"context"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Kind allowed as Replication source

const KindSecret = "Secret"
const KindConfigMap = "ConfigMap"

// Annotations set on the replicated (destination) resource. ReplicationAnnotation also acts as a
// marker to ensure we never overwrite a resource managed by another Replication.

const ReplicationAnnotation = "kubocd.kubotal.io/replication"
const ReplicationSourceAnnotation = "kubocd.kubotal.io/replication-source"

// ReplicationReconcilePeriod is the period of the systematic reconciliation. It also caps the retry
// backoff (See the controller rate limiter setup), to allow recovery from conditions we can't watch,
// the typical one being a destination namespace which does not exist yet.
const ReplicationReconcilePeriod = time.Minute * 2

// ResourceIndexOnReplication indexes a Replication on both its source and its destination resource,
// to allow retrieving the Replications to reconcile on a Secret/ConfigMap event.
const ResourceIndexOnReplication = "resourceIndexOnReplication"

// ResourceKey builds the ResourceIndexOnReplication key of a Secret or a ConfigMap.
func ResourceKey(namespace, kind, name string) string {
	return fmt.Sprintf("%s/%s/%s", namespace, kind, name)
}

// ReplicationResourceKeys returns the ResourceIndexOnReplication keys of a Replication: the one of its
// source, and the one of its destination, as a modification of any of them must trigger a reconciliation.
func ReplicationResourceKeys(replication *kv1alpha1.Replication) []string {
	destination := replicationDestination(replication)
	return []string{
		ResourceKey(replication.GetNamespace(), replication.Spec.Source.Kind, replication.Spec.Source.Name),
		ResourceKey(destination.Namespace, replication.Spec.Source.Kind, destination.Name),
	}
}

// replicationDestination returns the destination of a Replication, with the name defaulted to the source one.
func replicationDestination(replication *kv1alpha1.Replication) types.NamespacedName {
	destination := types.NamespacedName{
		Namespace: replication.Spec.Destination.Namespace,
		Name:      replication.Spec.Destination.Name,
	}
	if destination.Name == "" {
		// Default to source name
		destination.Name = replication.Spec.Source.Name
	}
	return destination
}

type ReplicationReconciler struct {
	client.Client
	record.EventRecorder
	Logger logr.Logger
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=replications,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=replications/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=replications/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets;configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ReplicationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ReplicationReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	//logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)

	replication := &kv1alpha1.Replication{}
	err := r.Get(ctx, req.NamespacedName, replication)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// NB: A finalizer is required, as the replicated resource lies in another namespace. So, its deletion
	// can't be delegated to the Kubernetes garbage collector, which does not handle cross namespace ownership.

	if !replication.ObjectMeta.DeletionTimestamp.IsZero() {
		// Deletion is requested
		if !controllerutil.ContainsFinalizer(replication, global.FinalizerName) {
			// No finalizer at all. Nothing to do anymore
			return ctrl.Result{}, nil
		}
		logger.V(0).Info("Deleting replication")
		// Perform deletion cleanup.
		if err := r.deleteReplicated(ctx, replication.Status.Destination, replication, logger); err != nil {
			// Retry, as giving up here would leave an orphan resource behind
			logger.V(0).Error(err, "unable to delete replicated resource", "replication", req.NamespacedName.String())
			return ctrl.Result{}, err
		}
		// Deletion OK
		controllerutil.RemoveFinalizer(replication, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Remove finalizer)")
		if err := r.Update(ctx, replication); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Not under deletion. Add a finalizer if not already set
	if !controllerutil.ContainsFinalizer(replication, global.FinalizerName) {
		logger.V(1).Info("Add finalizer")
		controllerutil.AddFinalizer(replication, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Add finalizer)")
		err := r.Update(ctx, replication)
		return ctrl.Result{}, err // we reschedule, to avoid an 'object has been modified' on next status update
	}

	previous := replication.DeepCopy()

	//-------------------
	/*
	   Aim of this controller is to duplicate the Source to the Destination
	   Source.Kind can only be Secret and ConfigMap.
	   Replication process must be smart:
	   - Report error by setting status.phase to ERROR and status.message to a human comprehensible message.
	   - Update Destination only if missing or different.
	   - Be idempotent
	*/
	/*
		- Implement internal.controller.replication_controller.go following instruction in comments (Line 54)
		- add the watch on secrets and configmaps
		- If a replication update generate a different destination, the previous one remains. Same if replication is deleted. Could you fix this
		- Also, if source object is deleted, then delete destination
		- Add a status.destinationName, always set, internded to be displayed in kubebuilder.printcolumn

	*/

	// Always exposed, even if the replication fails, as this is the name the destination would have.
	replication.Status.DestinationName = replicationDestination(replication).Name

	retryable, replicationError := r.replicate(ctx, replication, logger)
	if replicationError != nil {
		logger.V(0).Error(replicationError, "unable to replicate", "replication", req.NamespacedName.String())
		message := replicationError.Error()
		if previous.Status.Phase != kv1alpha1.ReplicationPhaseError || previous.Status.Message != message {
			r.Event(replication, "Warning", "Status", message)
		}
		replication.Status.Phase = kv1alpha1.ReplicationPhaseError
		replication.Status.Message = message
	} else {
		if previous.Status.Phase != kv1alpha1.ReplicationPhaseReady {
			r.Event(replication, "Normal", "Status", "Replication ready")
		}
		replication.Status.Phase = kv1alpha1.ReplicationPhaseReady
		replication.Status.Message = ""
	}

	if !reflect.DeepEqual(previous.Status, replication.Status) {
		// Status modified. (Using Patch does not prevent an unnecessary round trip)
		err = r.Status().Patch(ctx, replication, client.MergeFrom(previous))
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	if retryable {
		// Recoverable problem (API error, or a destination namespace not created yet). Returning the error
		// requeues with a backoff, which the controller rate limiter caps at ReplicationReconcilePeriod.
		return ctrl.Result{}, replicationError
	}
	if replicationError != nil {
		// Permanent error state. It can only be solved by updating this object, or the source/destination
		// resource. All of them are watched, so there is nothing to poll for.
		return ctrl.Result{}, nil
	}
	// In sync. Reconcile periodically anyway, to catch up on what the watches may have missed.
	return ctrl.Result{RequeueAfter: ReplicationReconcilePeriod}, nil
}

// replicate performs the copy of the source to the destination. It returns a flag indicating if the
// returned error (if any) is worth a retry, and the error itself, with a message aimed to be displayed
// in the resource status.
func (r *ReplicationReconciler) replicate(ctx context.Context, replication *kv1alpha1.Replication, logger logr.Logger) (bool, error) {
	sourceName := replication.Spec.Source.Name
	destination := replicationDestination(replication)
	target := &kv1alpha1.ReplicatedResource{
		Kind:      replication.Spec.Source.Kind,
		Namespace: destination.Namespace,
		Name:      destination.Name,
	}
	// The spec may have been modified to target another resource. In such case, the previously
	// replicated one is now an orphan, so must be removed.
	if replication.Status.Destination != nil && *replication.Status.Destination != *target {
		if err := r.deleteReplicated(ctx, replication.Status.Destination, replication, logger); err != nil {
			return true, err
		}
		replication.Status.Destination = nil
	}
	if destination.Namespace == replication.GetNamespace() && destination.Name == sourceName {
		return false, fmt.Errorf("destination '%s/%s' is the same as the source", destination.Namespace, destination.Name)
	}
	source := types.NamespacedName{Namespace: replication.GetNamespace(), Name: sourceName}
	var retryable bool
	var err error
	switch replication.Spec.Source.Kind {
	case KindSecret:
		retryable, err = r.replicateSecret(ctx, replication, source, destination, logger)
	case KindConfigMap:
		retryable, err = r.replicateConfigMap(ctx, replication, source, destination, logger)
	default:
		return false, fmt.Errorf("invalid source kind '%s'. Must be '%s' or '%s'", replication.Spec.Source.Kind, KindSecret, KindConfigMap)
	}
	if err != nil {
		return retryable, err
	}
	// Keep track of what we handle, to be able to remove it on spec modification or on deletion
	replication.Status.Destination = target
	return false, nil
}

// deleteReplicated removes a resource previously created by this Replication. Such resource is left
// untouched if it has been taken over by another Replication in the meantime.
func (r *ReplicationReconciler) deleteReplicated(ctx context.Context, replicated *kv1alpha1.ReplicatedResource, replication *kv1alpha1.Replication, logger logr.Logger) error {
	if replicated == nil {
		// Nothing was replicated so far
		return nil
	}
	var object client.Object
	switch replicated.Kind {
	case KindSecret:
		object = &corev1.Secret{}
	case KindConfigMap:
		object = &corev1.ConfigMap{}
	default:
		// Should never occur, as we only track what we effectively replicated
		logger.V(0).Info("Unable to delete replicated resource of unhandled kind", "kind", replicated.Kind, "namespace", replicated.Namespace, "name", replicated.Name)
		return nil
	}
	err := r.Get(ctx, types.NamespacedName{Namespace: replicated.Namespace, Name: replicated.Name}, object)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Already gone
			return nil
		}
		return fmt.Errorf("unable to fetch %s '%s/%s': %s", replicated.Kind, replicated.Namespace, replicated.Name, apiErrorMessage(err))
	}
	if !isReplicatedBy(object, replication) {
		logger.V(0).Info("Replicated resource is not ours anymore. Leaving it untouched", "kind", replicated.Kind, "namespace", replicated.Namespace, "name", replicated.Name)
		return nil
	}
	if err := r.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("unable to delete %s '%s/%s': %s", replicated.Kind, replicated.Namespace, replicated.Name, apiErrorMessage(err))
	}
	logger.V(0).Info("Replicated resource deleted", "kind", replicated.Kind, "namespace", replicated.Namespace, "name", replicated.Name)
	r.Event(replication, "Normal", "Replication", fmt.Sprintf("Deleted %s '%s/%s'", replicated.Kind, replicated.Namespace, replicated.Name))
	return nil
}

func (r *ReplicationReconciler) replicateSecret(ctx context.Context, replication *kv1alpha1.Replication, source, destination types.NamespacedName, logger logr.Logger) (bool, error) {
	src := &corev1.Secret{}
	if err := r.Get(ctx, source, src); err != nil {
		return r.sourceFetchError(ctx, replication, KindSecret, source, err, logger)
	}
	dst := &corev1.Secret{}
	err := r.Get(ctx, destination, dst)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return true, destinationFetchError(KindSecret, destination, err)
		}
		// Missing: Create it
		dst = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: destination.Namespace, Name: destination.Name},
			Type:       src.Type,
			Data:       src.Data,
		}
		setReplicatedMetadata(dst, replication, &src.ObjectMeta)
		if err := r.Create(ctx, dst); err != nil {
			return true, fmt.Errorf("unable to create %s '%s/%s': %s", KindSecret, destination.Namespace, destination.Name, apiErrorMessage(err))
		}
		logger.V(0).Info("Destination created", "kind", KindSecret, "namespace", destination.Namespace, "name", destination.Name)
		r.Event(replication, "Normal", "Replication", fmt.Sprintf("Created %s '%s/%s'", KindSecret, destination.Namespace, destination.Name))
		return false, nil
	}
	if err := checkDestinationOwnership(KindSecret, dst, replication); err != nil {
		return false, err
	}
	if dst.Type != src.Type {
		// Secret type is immutable. Only the user can solve this, by removing the destination.
		return false, fmt.Errorf("existing %s '%s/%s' is of type '%s' while source is of type '%s'", KindSecret, destination.Namespace, destination.Name, dst.Type, src.Type)
	}
	updated := dst.DeepCopy()
	updated.Data = src.Data
	setReplicatedMetadata(updated, replication, &src.ObjectMeta)
	if reflect.DeepEqual(dst.Data, updated.Data) && sameMetadata(&dst.ObjectMeta, &updated.ObjectMeta) {
		// Up to date. Nothing to do
		logger.V(1).Info("Destination unchanged", "kind", KindSecret, "namespace", destination.Namespace, "name", destination.Name)
		return false, nil
	}
	// NB: Update (and not Patch) as we also want removed source entries to be removed from the destination.
	if err := r.Update(ctx, updated); err != nil {
		return true, fmt.Errorf("unable to update %s '%s/%s': %s", KindSecret, destination.Namespace, destination.Name, apiErrorMessage(err))
	}
	logger.V(0).Info("Destination updated", "kind", KindSecret, "namespace", destination.Namespace, "name", destination.Name)
	r.Event(replication, "Normal", "Replication", fmt.Sprintf("Updated %s '%s/%s'", KindSecret, destination.Namespace, destination.Name))
	return false, nil
}

func (r *ReplicationReconciler) replicateConfigMap(ctx context.Context, replication *kv1alpha1.Replication, source, destination types.NamespacedName, logger logr.Logger) (bool, error) {
	src := &corev1.ConfigMap{}
	if err := r.Get(ctx, source, src); err != nil {
		return r.sourceFetchError(ctx, replication, KindConfigMap, source, err, logger)
	}
	dst := &corev1.ConfigMap{}
	err := r.Get(ctx, destination, dst)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return true, destinationFetchError(KindConfigMap, destination, err)
		}
		// Missing: Create it
		dst = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: destination.Namespace, Name: destination.Name},
			Data:       src.Data,
			BinaryData: src.BinaryData,
		}
		setReplicatedMetadata(dst, replication, &src.ObjectMeta)
		if err := r.Create(ctx, dst); err != nil {
			return true, fmt.Errorf("unable to create %s '%s/%s': %s", KindConfigMap, destination.Namespace, destination.Name, apiErrorMessage(err))
		}
		logger.V(0).Info("Destination created", "kind", KindConfigMap, "namespace", destination.Namespace, "name", destination.Name)
		r.Event(replication, "Normal", "Replication", fmt.Sprintf("Created %s '%s/%s'", KindConfigMap, destination.Namespace, destination.Name))
		return false, nil
	}
	if err := checkDestinationOwnership(KindConfigMap, dst, replication); err != nil {
		return false, err
	}
	updated := dst.DeepCopy()
	updated.Data = src.Data
	updated.BinaryData = src.BinaryData
	setReplicatedMetadata(updated, replication, &src.ObjectMeta)
	if reflect.DeepEqual(dst.Data, updated.Data) && reflect.DeepEqual(dst.BinaryData, updated.BinaryData) && sameMetadata(&dst.ObjectMeta, &updated.ObjectMeta) {
		// Up to date. Nothing to do
		logger.V(1).Info("Destination unchanged", "kind", KindConfigMap, "namespace", destination.Namespace, "name", destination.Name)
		return false, nil
	}
	// NB: Update (and not Patch) as we also want removed source entries to be removed from the destination.
	if err := r.Update(ctx, updated); err != nil {
		return true, fmt.Errorf("unable to update %s '%s/%s': %s", KindConfigMap, destination.Namespace, destination.Name, apiErrorMessage(err))
	}
	logger.V(0).Info("Destination updated", "kind", KindConfigMap, "namespace", destination.Namespace, "name", destination.Name)
	r.Event(replication, "Normal", "Replication", fmt.Sprintf("Updated %s '%s/%s'", KindConfigMap, destination.Namespace, destination.Name))
	return false, nil
}

// setReplicatedMetadata aligns the destination labels and annotations on the source ones, and adds our
// own markers. Labels and annotations set on the destination by someone else are dropped, so a value
// removed from the source is also removed from the destination.
func setReplicatedMetadata(destination client.Object, replication *kv1alpha1.Replication, source *metav1.ObjectMeta) {
	labels := make(map[string]string, len(source.Labels))
	for key, value := range source.Labels {
		labels[key] = value
	}
	annotations := make(map[string]string, len(source.Annotations)+2)
	for key, value := range source.Annotations {
		if key == corev1.LastAppliedConfigAnnotation {
			// Would be misleading on the destination
			continue
		}
		annotations[key] = value
	}
	annotations[ReplicationAnnotation] = replicationOwner(replication)
	annotations[ReplicationSourceAnnotation] = fmt.Sprintf("%s/%s/%s", replication.GetNamespace(), replication.Spec.Source.Kind, replication.Spec.Source.Name)
	if len(labels) == 0 {
		labels = nil
	}
	destination.SetLabels(labels)
	destination.SetAnnotations(annotations)
}

func sameMetadata(current, target *metav1.ObjectMeta) bool {
	return reflect.DeepEqual(current.Labels, target.Labels) && reflect.DeepEqual(current.Annotations, target.Annotations)
}

// checkDestinationOwnership ensures we are not about to overwrite a resource handled by another Replication.
// A resource which is not the result of a replication is adopted.
func checkDestinationOwnership(kind string, destination client.Object, replication *kv1alpha1.Replication) error {
	owner, ok := destination.GetAnnotations()[ReplicationAnnotation]
	if !ok {
		return nil
	}
	if owner != replicationOwner(replication) {
		return fmt.Errorf("existing %s '%s/%s' is already replicated by '%s'", kind, destination.GetNamespace(), destination.GetName(), owner)
	}
	return nil
}

// isReplicatedBy checks a resource is effectively the one we replicated. As, unlike the update case, deletion
// is not recoverable, we require an explicit ownership marker.
func isReplicatedBy(object client.Object, replication *kv1alpha1.Replication) bool {
	return object.GetAnnotations()[ReplicationAnnotation] == replicationOwner(replication)
}

func replicationOwner(replication *kv1alpha1.Replication) string {
	return fmt.Sprintf("%s/%s", replication.GetNamespace(), replication.GetName())
}

// sourceFetchError qualifies a failure to fetch the source. When the source is gone, what we replicated
// from it must not survive it, so it is deleted. And there is nothing to retry, as the Secret/ConfigMap
// watch will trigger a new reconciliation if the source shows up again.
func (r *ReplicationReconciler) sourceFetchError(ctx context.Context, replication *kv1alpha1.Replication, kind string, source types.NamespacedName, err error, logger logr.Logger) (bool, error) {
	if apierrors.IsNotFound(err) {
		if err := r.deleteReplicated(ctx, replication.Status.Destination, replication, logger); err != nil {
			return true, err
		}
		replication.Status.Destination = nil
		return false, fmt.Errorf("source %s '%s/%s' does not exists", kind, source.Namespace, source.Name)
	}
	return true, fmt.Errorf("unable to fetch source %s '%s/%s': %s", kind, source.Namespace, source.Name, apiErrorMessage(err))
}

func destinationFetchError(kind string, destination types.NamespacedName, err error) error {
	return fmt.Errorf("unable to fetch %s '%s/%s': %s", kind, destination.Namespace, destination.Name, apiErrorMessage(err))
}

// apiErrorMessage extracts a human comprehensible message from a Kubernetes API error.
func apiErrorMessage(err error) string {
	if status := apierrors.APIStatus(nil); errors.As(err, &status) {
		return status.Status().Message
	}
	return err.Error()
}
