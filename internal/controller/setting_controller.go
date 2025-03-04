package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// SettingReconciler reconciles a Setting object
type SettingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	record.EventRecorder
	Logger logr.Logger
}

// Just a container to avoid messy parameters passing
type settingOperation struct {
	ctx     context.Context
	logger  logr.Logger
	setting *kv1alpha1.Setting
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=settings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=settings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=settings/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *SettingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info("vv..............vv")
	result, err := r.reconcile2(ctx, req, logger)
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *SettingReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	setting := &kv1alpha1.Setting{}
	err := r.Get(ctx, req.NamespacedName, setting)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	op := &settingOperation{
		ctx:     ctx,
		logger:  logger,
		setting: setting,
	}

	// We have nothing to cleanup with this kind. So no need to setup a finalizer
	upd := setting.DeepCopy()
	dirty, err := groomSetting(upd, logger)
	if err != nil {
		return r.reportError(op, err, true, "InvalidData")
	}
	if dirty {
		err = r.Update(ctx, upd)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	if len(setting.Spec.Parents) == 0 {
		// Must ensure status is empty
		if setting.Status.Context != nil || setting.Status.OciRedirects != nil || setting.Status.ClusterRoles != nil {
			setting.Status.Context = nil
			setting.Status.OciRedirects = nil
			setting.Status.ClusterRoles = nil
			return ctrl.Result{}, r.updatePhase(op, kv1alpha1.SettingPhaseReady, true)
		}
	} else {
		return ctrl.Result{}, r.updatePhase(op, kv1alpha1.SettingPhaseReady, true)
		//// Get parent
		//for _, parent := range setting.Spec.Parents {
		//	parentSetting := &kv1alpha1.Setting{}
		//	err = r.Get(ctx, parent.ToObjectKey(), parentSetting)
		//	if err != nil {
		//		if errors.IsNotFound(err) {
		//			return r.reportError(op, err, true, "GetParent")
		//		} else {
		//			return r.reportError(op, fmt.Errorf(fmt.Sprintf("Parent '%s' not found", parent.String())), false, "MissingParent")
		//		}
		//	}
		//	if parentSetting.Status.Phase != kv1alpha1.SettingPhaseReady {
		//		return r.reportError(op, fmt.Errorf(fmt.Sprintf("Parent '%s' is in error", parent.String())), false, "ParentError")
		//	}
		//	// OK. Merge our info on top of our parent
		//	ctx := parentSetting.Status.Context
		//	if ctx == nil {
		//		ctx = parentSetting.Spec.Context
		//	}
		//	base :=
		//
		//}

		// Must build status
	}
	return ctrl.Result{}, nil
}

//
//func mergeSettings(parent *kv1alpha1.Setting, child *kv1alpha1.Setting) (*apiextensionsv1.JSON, []kv1alpha1.OciRedirectSpec, []string, error) {
//	// --------------------------------Handle context
//	ctx := parent.Status.Context
//	if ctx == nil {
//		ctx = parent.Spec.Context
//	}
//	base := make(map[string]interface{})
//	err := yaml.UnmarshalStrict(ctx.Raw, base)
//	if err != nil {
//		return nil, nil, nil, err // Should not occurs, as parent should be in error
//	}
//	my := make(map[string]interface{})
//	err = yaml.UnmarshalStrict(child.Spec.Context.Raw, my)
//	if err != nil {
//		return nil, nil, nil, err // Should not occurs, as parent should be in error
//	}
//	r := misc.MergeMaps(base, my)
//	newCtx, err := yaml.Marshal(r)
//	if err != nil {
//		return nil, nil, nil, err // Should not occurs
//	}
//	// ------------------------------
//	redirects := parent.Status.OciRedirects
//	if redirects == nil {
//		redirects = parent.Spec.OciRedirects
//	}
//	newRedirects := append(redirects, child.Spec.OciRedirects...)
//	// -------------------------------
//	clusterRoles := parent.Status.ClusterRoles
//	if clusterRoles == nil {
//		clusterRoles = parent.Spec.ClusterRoles
//	}
//	newClusterRoles := append(clusterRoles, child.Spec.ClusterRoles...)
//	return &apiextensionsv1.JSON{Raw: newCtx}, newRedirects, newClusterRoles, nil
//
//}

func groomSetting(setting *kv1alpha1.Setting, logger logr.Logger) (dirty bool, err error) {
	dirty = false
	for i := range setting.Spec.Parents {
		child := &setting.Spec.Parents[i]
		if child.Namespace == "" {
			logger.V(1).Info("Set namespace for child", "name", child.Name, "namespace", setting.ObjectMeta.Namespace)
			child.Namespace = setting.ObjectMeta.Namespace
			dirty = true
		}
	}
	// Check context is a valid map
	kuboContext := make(map[string]interface{})
	err = yaml.UnmarshalStrict(setting.Spec.Context.Raw, &kuboContext)
	if err != nil {
		return false, fmt.Errorf("unmarshalling context: %w", err)
	}
	return dirty, nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *SettingReconciler) reportError(op *settingOperation, err error, fatal bool, eventReason string) (ctrl.Result, error) {
	err2 := r.updatePhase(op, kv1alpha1.SettingPhaseError, false)
	if err2 != nil {
		return ctrl.Result{}, err // Will retry
	}
	if eventReason != "" && err != nil {
		r.Event(op.setting, "Warning", eventReason, err.Error())
	}
	if fatal {
		op.logger.Error(err, "Wait for this to be fixed")
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{}, err
	}
}

func (r *SettingReconciler) updatePhase(op *settingOperation, phase kv1alpha1.SettingPhase, force bool) error {
	if op.setting.Status.Phase == phase && !force {
		op.logger.V(1).Info("Setting phase is already up-to-date", "phase", phase)
		return nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.setting.Status.Phase, "force", force)
	op.setting.Status.Phase = phase
	return r.Status().Update(op.ctx, op.setting)
}
