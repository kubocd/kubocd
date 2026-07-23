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

// Aim of this controller is only to validate its spec and handle error

type ClusterInterfaceReconciler struct {
	client.Client
	record.EventRecorder
	Logger logr.Logger
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=interfaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=interfaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=interfaces/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ClusterInterfaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ClusterInterfaceReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	//logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)

	clusterIface := &kv1alpha1.ClusterInterface{}
	err := r.Get(ctx, req.NamespacedName, clusterIface)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// NB: As there is no non-k8s related object, there is no need for finalizer

	previous := clusterIface.DeepCopy()

	_, _, err = resolveInterface(clusterIface)
	if err != nil {
		logger.V(0).Error(err, "Error on interface", "interface", clusterIface.Name)
		r.Event(clusterIface, "Warning", "Invalid", err.Error())
		clusterIface.Status.Phase = kv1alpha1.InterfacePhaseError
		clusterIface.Status.Message = err.Error()
	} else {
		if previous.Status.Phase != kv1alpha1.InterfacePhaseReady {
			r.Event(clusterIface, "Normal", "OK", "interface ok")
		}
		clusterIface.Status.Phase = kv1alpha1.InterfacePhaseReady
		clusterIface.Status.Message = ""
	}
	if reflect.DeepEqual(previous.Status, clusterIface.Status) {
		// Status unmodified. End of works (Using Patch does not prevent an unnecessary round trip)
		return ctrl.Result{}, nil
	}
	err = r.Status().Patch(ctx, clusterIface, client.MergeFrom(previous))
	if err != nil {
		return ctrl.Result{}, err
	}
	// Even in case of error, we don't retry, as the only way to fix is to update object.
	return ctrl.Result{}, nil
}
