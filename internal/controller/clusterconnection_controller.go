package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"reflect"

	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Aim of this controller is to validate its spec against contract and handle error

// ClusterConnectionReconciler feed connectionStore (Memory storage) with connections
type ClusterConnectionReconciler struct {
	client.Client
	record.EventRecorder
	Logger logr.Logger
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=clusterconnections,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=clusterconnections/status,verbs=patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ClusterConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ClusterConnectionReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)

	clusterConnection := &kv1alpha1.ClusterConnection{}
	err := r.Get(ctx, req.NamespacedName, clusterConnection)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// NB: As there is no non-k8s related object, there is no need for finalizer

	var finalError error = nil
	previous := clusterConnection.DeepCopy()

	clusterContract, err := findContract(ctx, r, clusterConnection)
	if err != nil {
		return ctrl.Result{}, err
	}
	if clusterContract == nil {
		// No corresponding ClusterContract found
		if err := connectionError(clusterContract, clusterConnection); err == nil {
			// If no Values, this is legal
			if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				r.Event(clusterConnection, "Normal", "Status", "ClusterConnection ready")
			}
			clusterConnection.Status.Phase = kv1alpha1.ConnectionPhaseReady
			clusterConnection.Status.Message = ""
			finalError = nil
		} else {
			clusterConnection.Status.Phase = kv1alpha1.ConnectionPhaseError
			message := err.Error()
			if clusterConnection.Status.Message != message {
				r.Event(clusterConnection, "Warning", "Status", message)
			}
			clusterConnection.Status.Message = message
			finalError = err
		}
	} else {
		if err := checkConnection(clusterContract, clusterConnection); err != nil {
			logger.V(0).Error(err, "unable to validate clusterConnection", "clusterConnection", req.NamespacedName.String())
			message := err.Error()
			if clusterConnection.Status.Message != message {
				r.Event(clusterConnection, "Warning", "Status", message)
			}
			clusterConnection.Status.Phase = kv1alpha1.ConnectionPhaseError
			clusterConnection.Status.Message = message
			finalError = err
		} else {
			if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				r.Event(clusterConnection, "Normal", "Status", "ClusterConnection ready")
			}
			clusterConnection.Status.Phase = kv1alpha1.ConnectionPhaseReady
			clusterConnection.Status.Message = ""
			finalError = nil
		}
		clusterConnection.Status.ContractGeneration = clusterContract.GetGeneration()
	}
	if clusterConnection.Status.Parent == "" && clusterConnection.Spec.ParentRelease != nil {
		clusterConnection.Status.Parent = fmt.Sprintf("%s/%s", clusterConnection.Spec.ParentRelease.Namespace, clusterConnection.Spec.ParentRelease.Name)
	}
	if reflect.DeepEqual(previous.Status, clusterConnection.Status) {
		// Status unmodified. End of works (Using Patch does not prevent an unnecessary round trip)
		return ctrl.Result{}, finalError
	}
	err = r.Status().Patch(ctx, clusterConnection, client.MergeFrom(previous))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("could not update the ClusterConnection status: %s", apiError(err))
	}
	return ctrl.Result{}, finalError
}
