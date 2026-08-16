package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kuboschema"
	"reflect"

	"github.com/go-logr/logr"
	"github.com/xeipuuv/gojsonschema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Aim of this controller is only to validate its spec and handle error

type ContractReconciler struct {
	client.Client
	record.EventRecorder
	Logger logr.Logger
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contracts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contracts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contracts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ContractReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ContractReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	//logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)

	contract := &kv1alpha1.Contract{}
	err := r.Get(ctx, req.NamespacedName, contract)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// NB: As there is no non-k8s related object, there is no need for finalizer

	previous := contract.DeepCopy()

	_, _, err = resolveContract(contract)
	if err != nil {
		logger.V(0).Error(err, "Error on contract", "contract", contract.Name)
		r.Event(contract, "Warning", "Invalid", err.Error())
		contract.Status.Phase = kv1alpha1.ContractPhaseError
		contract.Status.Message = err.Error()
	} else {
		if previous.Status.Phase != kv1alpha1.ContractPhaseReady {
			r.Event(contract, "Normal", "OK", "contract ok")
		}
		contract.Status.Phase = kv1alpha1.ContractPhaseReady
		contract.Status.Message = ""
	}
	if reflect.DeepEqual(previous.Status, contract.Status) {
		// Status unmodified. End of works (Using Patch does not prevent an unnecessary round trip)
		return ctrl.Result{}, nil
	}
	err = r.Status().Patch(ctx, contract, client.MergeFrom(previous))
	if err != nil {
		return ctrl.Result{}, err
	}
	// Even in case of error, we don't retry, as the only way to fix is to update object.
	return ctrl.Result{}, nil
}

func resolveContract(contract kv1alpha1.ContractFacade) (defaultValues map[string]interface{}, goSchema *gojsonschema.Schema, err error) {
	// Convert k8s form to KuboSchema
	sch := make(kuboschema.KuboSchema)
	if contract.GetSchemaRaw() != nil {
		err = yaml.UnmarshalStrict(contract.GetSchemaRaw(), &sch)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to parse schema: %w", err)
		}
	} else {
		err = yaml.UnmarshalStrict([]byte(`{ "properties": {} }`), &sch)
		if err != nil {
			return nil, nil, fmt.Errorf("unable to parse empty schema: %w", err)
		}
	}
	// Translate potential KubocdSchema to JSON/openAPI format
	sch, err = kuboschema.Kubo2openAPI(sch, false)
	if err != nil {
		return nil, nil, fmt.Errorf("error on kubocd schema conversion: %w", err)
	}
	// Test default value
	defaultValues, err = kuboschema.Defaulter(sch)
	if err != nil {
		return nil, nil, fmt.Errorf("error on default values setting: %w", err)

	}
	// And test schema compilation
	goSchema, err = gojsonschema.NewSchema(gojsonschema.NewGoLoader(sch))
	if err != nil {
		return nil, nil, fmt.Errorf("error on schema compilation: %w", err)
	}
	return defaultValues, goSchema, err
}
