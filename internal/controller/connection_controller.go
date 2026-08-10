package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"reflect"

	"github.com/go-logr/logr"
	"github.com/xeipuuv/gojsonschema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	var iface kv1alpha1.InterfaceFacade

	iface = &kv1alpha1.Interface{}
	err = r.Get(ctx, types.NamespacedName{Name: connection.Spec.Interface, Namespace: connection.GetNamespace()}, iface)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// Not Found. Try ClusterInterface
		iface = &kv1alpha1.ClusterInterface{}
		err = r.Get(ctx, types.NamespacedName{Name: connection.Spec.Interface}, iface)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			iface = nil // Mark not found
		}
	}
	if iface == nil {
		if misc.IsZero(connection.Spec.Values) {
			// If no Values, this is legal
			if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				r.Event(connection, "Normal", "Status", "Connection ready")
			}
			connection.Status.Phase = kv1alpha1.ConnectionPhaseReady
			connection.Status.Message = ""
			finalError = nil
		} else {
			connection.Status.Phase = kv1alpha1.ConnectionPhaseError
			connection.Status.InterfaceKind = ""
			message := fmt.Sprintf("Interface or ClusterInterface '%s' missing", connection.Spec.Interface)
			if connection.Status.Message != message {
				r.Event(connection, "Warning", "Status", message)
			}
			connection.Status.Message = message
			finalError = fmt.Errorf("interface/clusterInterface '%s' missing", connection.Spec.Interface)
		}
	} else {
		if err := checkConnection(iface, connection); err != nil {
			logger.V(0).Error(err, "unable to validate connection", "connection", req.NamespacedName.String())
			message := err.Error()
			if connection.Status.Message != message {
				r.Event(connection, "Warning", "Status", message)
			}
			connection.Status.Phase = kv1alpha1.ConnectionPhaseError
			connection.Status.Message = message
			finalError = err
		} else {
			if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				r.Event(connection, "Normal", "Status", "Connection ready")
			}
			connection.Status.Phase = kv1alpha1.ConnectionPhaseReady
			connection.Status.Message = ""
			finalError = nil
		}
		connection.Status.InterfaceGeneration = iface.GetGeneration()
		connection.Status.InterfaceKind = iface.GetKind()
	}
	// For prettier interface display
	switch connection.Status.InterfaceKind {
	case kv1alpha1.KindInterface:
		connection.Status.InterfaceDisplay = connection.Spec.Interface
	case kv1alpha1.KindClusterInterface:
		connection.Status.InterfaceDisplay = fmt.Sprintf("[%s]", connection.Spec.Interface)
	default:
		connection.Status.InterfaceDisplay = fmt.Sprintf("%s?", connection.Spec.Interface)
	}
	if connection.Status.Parent == "" {
		owner := metav1.GetControllerOf(connection)
		if owner == nil {
			connection.Status.Parent = ""
		} else {
			connection.Status.Parent = owner.Name
		}
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

func checkConnection(iface kv1alpha1.InterfaceFacade, connection kv1alpha1.ConnectionFacade) error {
	if iface.GetStatusPhase() != kv1alpha1.InterfacePhaseReady {
		return fmt.Errorf("interface is not ready")
	}
	defaultValue, goSchema, err := resolveInterface(iface)
	if err != nil {
		// NB: This should newer occurs, as interface should be in error case.
		return fmt.Errorf("interface in error: %w", err)
	}
	values := make(map[string]interface{})
	if connection.GetValuesRaw() != nil {
		err = yaml.UnmarshalStrict(connection.GetValuesRaw(), &values)
		if err != nil {
			return fmt.Errorf("unable to parse connection values: %w", err)
		}
	}
	values = misc.MergeMaps(defaultValue, values)
	// Must check against interface schema
	validate, err := goSchema.Validate(gojsonschema.NewGoLoader(values))
	if err != nil {
		return fmt.Errorf("error on values: %w", err)
	}
	if len(validate.Errors()) > 0 {
		return fmt.Errorf("validation error on values: %s", validate.Errors()[0])
	}
	return nil
}
