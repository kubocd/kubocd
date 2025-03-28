package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConfigReconciler load all configs to sore in configstore.ConfigStore
type ConfigReconciler struct {
	client.Client
	record.EventRecorder
	Logger         logr.Logger
	ConfigStore    configstore.ConfigStore
	MyPodNamespace string
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=configs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=configs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=configs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info(fmt.Sprintf("vv..............vv  %s:%s", req.NamespacedName.Namespace, req.NamespacedName.Name))
	result, err := r.reconcile2(ctx, req, logger)
	// result := ctrl.Result{}
	// var err error = nil
	logger.V(1).Info("^^..............^^", "result", result)
	return result, err
}

func (r *ConfigReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't care about who trigger this. We fetch all configs which are in our namespace and store them in configStore
	configs := &kv1alpha1.ConfigList{}
	err := r.List(ctx, configs, client.InNamespace(r.MyPodNamespace))
	if err != nil {
		return ctrl.Result{}, err
	}
	r.ConfigStore.AddConfigs(configs, r.MyPodNamespace)
	return ctrl.Result{}, nil
}
