package controller

import (
	"context"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
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

// Aim of this controller is to validate its spec against contract and handle error

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

	contract, err := findContract(ctx, r, connection)
	if err != nil {
		return ctrl.Result{}, err
	}
	if contract == nil {
		if err := connectionError(contract, connection); err == nil {
			// If no Values, this is legal
			if previous.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				r.Event(connection, "Normal", "Status", "Connection ready")
			}
			connection.Status.Phase = kv1alpha1.ConnectionPhaseReady
			connection.Status.Message = ""
			finalError = nil
		} else {
			connection.Status.Phase = kv1alpha1.ConnectionPhaseError
			connection.Status.ContractKind = ""
			message := err.Error()
			if connection.Status.Message != message {
				r.Event(connection, "Warning", "Status", message)
			}
			connection.Status.Message = message
			finalError = err
		}
	} else {
		if err := checkConnection(contract, connection); err != nil {
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
		connection.Status.ContractGeneration = contract.GetGeneration()
		connection.Status.ContractKind = contract.GetKind()
	}
	// For prettier contract display
	switch connection.Status.ContractKind {
	case kv1alpha1.KindContract:
		connection.Status.ContractDisplay = connection.Spec.Contract
	case kv1alpha1.KindClusterContract:
		connection.Status.ContractDisplay = fmt.Sprintf("[%s]", connection.Spec.Contract)
	default:
		connection.Status.ContractDisplay = fmt.Sprintf("%s?", connection.Spec.Contract)
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
		return ctrl.Result{}, fmt.Errorf("could not update the Connection status: %s", apiError(err))
	}
	return ctrl.Result{}, finalError
}

// findContract returns the contract that connection names: the Contract of
// its namespace, or else the ClusterContract, for a Connection, and the
// ClusterContract for a ClusterConnection. It is nil when there is none. A
// refused read gives the reason of the answer only, as an output renders the
// name of its contract.
func findContract(ctx context.Context, reader client.Reader, connection kv1alpha1.ConnectionFacade) (kv1alpha1.ContractFacade, error) {
	if connection.GetKind() == kv1alpha1.KindConnection {
		contract := &kv1alpha1.Contract{}
		err := reader.Get(ctx, types.NamespacedName{Name: connection.GetContract(), Namespace: connection.GetNamespace()}, contract)
		if err == nil {
			return contract, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("could not get the Contract: %s", apiError(err))
		}
	}
	clusterContract := &kv1alpha1.ClusterContract{}
	err := reader.Get(ctx, types.NamespacedName{Name: connection.GetContract()}, clusterContract)
	if err == nil {
		return clusterContract, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("could not get the ClusterContract: %s", apiError(err))
	}
	return nil, nil
}

// connectionError checks connection against contract, nil when missing. It
// returns nil when connection is ready, and otherwise the error whose text
// the controller of connection writes in its status. A connection without
// values needs no contract.
func connectionError(contract kv1alpha1.ContractFacade, connection kv1alpha1.ConnectionFacade) error {
	if contract != nil {
		return checkConnection(contract, connection)
	}
	if misc.IsZero(connection.GetValuesRaw()) {
		return nil
	}
	if connection.GetKind() == kv1alpha1.KindClusterConnection {
		return errors.New("the ClusterContract named in spec.contract is missing")
	}
	return errors.New("the Contract or ClusterContract named in spec.contract is missing")
}

// checkConnection validates the values of connection against contract. Its
// errors give a place and a category, never a key or a value of the values,
// which a Release renders for its outputs.
func checkConnection(contract kv1alpha1.ContractFacade, connection kv1alpha1.ConnectionFacade) error {
	if contract.GetStatusPhase() != kv1alpha1.ContractPhaseReady {
		return fmt.Errorf("contract is not ready")
	}
	defaultValue, schema, goSchema, err := resolveContract(contract)
	if err != nil {
		// NB: This should newer occurs, as contract should be in error case.
		return fmt.Errorf("contract in error: %w", err)
	}
	values := make(map[string]interface{})
	if connection.GetValuesRaw() != nil {
		if yaml.UnmarshalStrict(connection.GetValuesRaw(), &values) != nil {
			return errors.New("unable to parse connection values: they are not a map with distinct keys")
		}
	}
	values = misc.MergeMaps(defaultValue, values)
	// Must check against contract schema
	validate, err := goSchema.Validate(gojsonschema.NewGoLoader(values))
	if err != nil {
		return errors.New("unable to check connection values against the contract")
	}
	if len(validate.Errors()) > 0 {
		return fmt.Errorf("validation error on values: %s", kubopackage.SchemaError(validate, schema, values))
	}
	return nil
}
