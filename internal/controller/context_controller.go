package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
	"strings"
)

// ContextReconciler reconciles a Context object
type ContextReconciler struct {
	client.Client
	//Scheme *runtime.Scheme
	record.EventRecorder
	Logger logr.Logger
}

// Just a container to avoid messy parameters passing
type contextOperation struct {
	ctx      context.Context
	logger   logr.Logger
	kcontext *kv1alpha1.Context
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contexts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contexts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=contexts/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ContextReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	// result := ctrl.Result{}
	// var err error = nil
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ContextReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	kcontext := &kv1alpha1.Context{}
	err := r.Get(ctx, req.NamespacedName, kcontext)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	op := &contextOperation{
		ctx:      ctx,
		logger:   logger,
		kcontext: kcontext,
	}

	// We have nothing to cleanup with this kind. So no need to setup a finalizer
	rErr := groomContext(kcontext, logger)
	if rErr != nil {
		return r.reportError(op, rErr)
	}
	if len(kcontext.Spec.Parents) == 0 {
		// Must ensure status is empty
		if kcontext.Status.Context != nil || kcontext.Status.Parents != "" {
			kcontext.Status.Context = nil
			kcontext.Status.Parents = ""
			return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, true)
		}
		return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, false)
	} else {
		// -------Build a displayable Parents list
		parents := make([]string, len(kcontext.Spec.Parents))
		for i := range kcontext.Spec.Parents {
			parents[i] = kcontext.Spec.Parents[i].String()
		}
		kcontext.Status.Parents = strings.Join(parents, ",")

		// ------ And now, build the current context
		base := make(map[string]interface{})
		for _, parent := range kcontext.Spec.Parents {
			parentContext := &kv1alpha1.Context{}
			err = r.Get(ctx, parent.ToObjectKey(), parentContext)
			if err != nil {
				if errors.IsNotFound(err) {
					return r.reportError(op, NewReconcileError(err, true, "GetParent"))
				} else {
					return r.reportError(op, NewReconcileError(fmt.Errorf(fmt.Sprintf("Parent '%s' not found", parent.String())), false, "MissingParent"))
				}
			}
			if parentContext.Status.Phase != kv1alpha1.ContextPhaseReady {
				return r.reportError(op, NewReconcileError(fmt.Errorf(fmt.Sprintf("Parent '%s' is in error", parent.String())), false, "ParentError"))
			}
			// OK. Merge our info on top of our parent
			ctx := parentContext.Status.Context
			if ctx == nil {
				ctx = parentContext.Spec.Context
			}
			base, err = Merge(base, ctx)
			if err != nil {
				return r.reportError(op, NewReconcileError(fmt.Errorf("unable to merge context from '%s': %w", parent.String(), err), true, "ContextError")) // Should not occurs
			}
		}
		// And merge out own content
		base, err = Merge(base, kcontext.Spec.Context)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to merge our own context values: %w", err), true, "ContextError")) // Should not occurs
		}
		// And store in status
		ba, err := json.Marshal(&base)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to marshal result"), false, "ContextError")) // Should not occurs
		}
		kcontext.Status.Context = &apiextensionsv1.JSON{
			Raw: ba,
		}
		return ctrl.Result{}, r.updatePhase(op, kv1alpha1.ContextPhaseReady, true)
	}
}

func groomContext(kcontext *kv1alpha1.Context, logger logr.Logger) ReconcileError {
	for i := range kcontext.Spec.Parents {
		child := &kcontext.Spec.Parents[i]
		if child.Namespace == "" {
			logger.V(1).Info("Set namespace for child", "childName", child.Name, "childNamespace", kcontext.ObjectMeta.Namespace)
			child.Namespace = kcontext.ObjectMeta.Namespace
		}
	}
	// Check context is a valid map
	kuboContext := make(map[string]interface{})
	err := yaml.UnmarshalStrict(kcontext.Spec.Context.Raw, &kuboContext)
	if err != nil {
		return NewReconcileError(fmt.Errorf("unable to unmarshal kubo context: %w", err), false, "InvalidContext")
	}
	return nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ContextReconciler) reportError(op *contextOperation, rErr ReconcileError) (ctrl.Result, error) {
	err2 := r.updatePhase(op, kv1alpha1.ContextPhaseError, false)
	if err2 != nil {
		return ctrl.Result{}, rErr // Will retry
	}
	if rErr.GetEventReason() != "" && rErr.GetBaseError() != nil {
		r.Event(op.kcontext, "Warning", rErr.GetEventReason(), rErr.Error())
	}
	if rErr.IsFatal() {
		op.logger.Error(rErr, "Wait for this to be fixed")
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{}, rErr
	}
}

func (r *ContextReconciler) updatePhase(op *contextOperation, phase kv1alpha1.ContextPhase, force bool) error {
	if op.kcontext.Status.Phase == phase && !force {
		op.logger.V(1).Info("Context phase is already up-to-date", "phase", phase)
		return nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.kcontext.Status.Phase, "force", force)
	op.kcontext.Status.Phase = phase
	return r.Status().Update(op.ctx, op.kcontext)
}
