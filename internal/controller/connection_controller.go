package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"reflect"

	"github.com/go-logr/logr"
	"github.com/xeipuuv/gojsonschema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Aim of this controller is to validate its spec against interface and handle error

// ConnectionReconciler feed connectionStore (Memory storage) with connections
type ConnectionReconciler struct {
	client.Client
	record.EventRecorder
	Logger logr.Logger
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=connections,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=connections/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=connections/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ConnectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ConnectionReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)

	connection := &kv1alpha1.Connection{}
	err := r.Get(ctx, req.NamespacedName, connection)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// NB: As there is no non-k8s related object, there is no need for finalizer

	var finalError error = nil
	previous := connection.DeepCopy()
	if connection.Spec.Disabled {
		if previous.Status.Phase != kv1alpha1.ConnectionPhaseDisabled {
			r.Event(connection, "Normal", "Status", "Set in DISABLED state")
		}
		connection.Status.Phase = kv1alpha1.ConnectionPhaseDisabled
		connection.Status.Message = ""
		connection.Status.InterfaceGeneration = 0
		finalError = nil
	} else if ifaceGeneration, err := r.checkConnection(ctx, connection); err != nil {
		logger.V(0).Error(err, "unable to register connection", "connection", req.NamespacedName.String())
		r.Event(connection, "Warning", "Registration", err.Error())
		connection.Status.Phase = kv1alpha1.ConnectionPhaseError
		connection.Status.Message = err.Error()
		connection.Status.InterfaceGeneration = ifaceGeneration
		finalError = err
	} else {
		if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
			r.Event(connection, "Normal", "Registration", "registration ok")
		}
		connection.Status.Phase = kv1alpha1.ConnectionPhaseReady
		connection.Status.Message = ""
		connection.Status.InterfaceGeneration = ifaceGeneration
		finalError = nil
	}
	if reflect.DeepEqual(previous.Status, connection.Status) {
		// Status unmodified. End of works (Using Patch does not prevent an unnecessary round trip)
		return ctrl.Result{}, finalError
	}
	err = r.Status().Patch(ctx, connection, client.MergeFrom(previous))
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, finalError
}

func (r *ConnectionReconciler) checkConnection(ctx context.Context, connection *kv1alpha1.Connection) (ifaceGeneration int64, err error) {
	iface := &kv1alpha1.Interface{}
	// Interface is cluster-scoped, so no namespace.
	err = r.Get(ctx, types.NamespacedName{Name: connection.Spec.Interface}, iface)
	if err != nil {
		return 0, fmt.Errorf("unable to fetch interface: %w", err)
	}
	if iface.Status.Phase != kv1alpha1.InterfacePhaseReady {
		return iface.Generation, fmt.Errorf("interface is not ready")
	}
	defaultValue, goSchema, err := resolveInterface(iface)
	if err != nil {
		// NB: This should newer occurs, as interface should be in error case.
		return iface.Generation, fmt.Errorf("interface in error: %w", err)
	}
	values := make(map[string]interface{})
	err = yaml.UnmarshalStrict(connection.Spec.Values.Raw, &values)
	if err != nil {
		return iface.Generation, fmt.Errorf("unable to parse connection values: %w", err)
	}
	values = misc.MergeMaps(defaultValue, values)
	// Must check against interface schema
	validate, err := goSchema.Validate(gojsonschema.NewGoLoader(values))
	if err != nil {
		return iface.Generation, fmt.Errorf("error on values: %w", err)
	}
	if len(validate.Errors()) > 0 {
		return iface.Generation, fmt.Errorf("validation error on values: %s", validate.Errors()[0])
	}
	return iface.Generation, nil
}
