/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	securejoin "github.com/cyphar/filepath-securejoin"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/http/fetch"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/service"
	"path"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	record.EventRecorder
	Logger         logr.Logger
	Fetcher        *fetch.ArchiveFetcher
	RootDataFolder string
}

// Just a container to avoid messy parameters passing
type operation struct {
	ctx     context.Context
	logger  logr.Logger
	release *kubocdv1alpha1.Release
}

// ReconcileError is a specialized error. Will allow to:
// - Specify if error is recoverable or not (fatal)
// - Specify we want to generate a Warning event.
type ReconcileError struct {
	error       error
	fatal       bool
	eventReason string
}

func (e ReconcileError) Error() string {
	return e.error.Error()
}

func NewReconcileError(err error, fatal bool, eventReason string) *ReconcileError {
	return &ReconcileError{
		error:       err,
		fatal:       fatal,
		eventReason: eventReason,
	}
}

// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubocd.kubotal.io,resources=releases/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Release object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Logger.WithValues("namespace", req.Namespace, "name", req.Name)
	logger.V(1).Info("vv--------------vv")
	result, err := r.reconcile2(ctx, req, logger)
	//logger.V(1).Info("^^--------------^^", "result", result, "error", err)
	logger.V(1).Info("^^--------------^^", "result", result)
	return result, err
}

func (r *ReleaseReconciler) reconcile2(ctx context.Context, req ctrl.Request, logger logr.Logger) (ctrl.Result, error) {
	// We don't use logger provided by the manager, as it is quite verbose
	//logger := log.FromContext(ctx)
	release := &kubocdv1alpha1.Release{}
	err := r.Get(ctx, req.NamespacedName, release)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	op := &operation{
		ctx:     ctx,
		logger:  logger,
		release: release,
	}

	if !release.ObjectMeta.DeletionTimestamp.IsZero() {
		// Deletion is requested
		if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
			// No finalizer at all. Nothing to do anymore
			return ctrl.Result{}, nil
		}
		logger.V(1).Info("Deleting release")
		// TODO: Perform deletion cleanup.
		// Deletion OK
		controllerutil.RemoveFinalizer(release, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Remove finalizer)")
		if err := r.Update(ctx, release); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	// Not under deletion. Add a finalizer if not already set
	if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
		logger.V(1).Info("Add finalizer")
		controllerutil.AddFinalizer(release, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Add finalizer)")
		err := r.Update(ctx, release)
		return ctrl.Result{}, err // we reschedule, to avoid an 'object has been modified' on next status update
		//if err != nil {
		//	return ctrl.Result{}, err
		//}
	}
	ociRepository, reconcileError := r.handleOciRepository(op, op.release.Name, global.ServiceManifestMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		err = r.updatePhase(op, kubocdv1alpha1.ReleasePhaseWaitingOci, false)
		if err != nil {
			return ctrl.Result{}, err // Will retry
		}
		// No need to requeue, as we should be notified when the OCI repo status will change
		//return ctrl.Result{RequeueAfter: time.Millisecond * 1000}, nil
		return ctrl.Result{}, nil
	}

	// ---------------------------------------------- At this point, we have an effective primary OCI repo.
	// So, we fetch the manifest.
	ociArtifact := ociRepository.Status.Artifact
	sourceLocation, err := securejoin.SecureJoin(r.RootDataFolder, global.SourceFolder)
	if err != nil {
		return ctrl.Result{}, err
	}
	err = misc.SafeEnsureEmpty(sourceLocation)
	if err != nil {
		return r.reportError(op, fmt.Errorf("unable to clean sourceLocation: %w", err), true, "LocalFS")
	}
	logger.V(1).Info("Will fetch artifact", "artifact.URL", ociArtifact.URL, "location", sourceLocation)
	err = r.Fetcher.Fetch(ociArtifact.URL, ociArtifact.Digest, sourceLocation)
	if err != nil {
		return r.reportError(op, fmt.Errorf("unable to fetch artifact: %w", err), false, "OCIRepository")
	}
	srv := &service.Service{}
	err = misc.LoadYaml(path.Join(sourceLocation, "manifest.yaml"), srv)
	if err != nil {
		return r.reportError(op, fmt.Errorf("error while parsing Manifest.yaml file: %w", err), true, "OCIImage")
	}

	fmt.Printf("Manifest: %s\n", misc.Map2YamlStr(srv))

	// ---------------------------------------------- Spawn secondary ociRepo
	for _, chart := range srv.Status.Charts {
		fmt.Printf("Chart %s\n", misc.Map2YamlStr(chart))
		ociRepoName := fmt.Sprintf("%s-%s", op.release.Name, chart.Module)
		_, reconcileError := r.handleOciRepository(op, ociRepoName, chart.MediaType, "copy")
		if reconcileError != nil {
			return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
		}
	}
	// --------------------------------------------- And spawn helmReleases
	for _, chart := range srv.Status.Charts {
		_, recErr := r.handleHelmRelease(op, chart.Module)
		if recErr != nil {
			return r.reportError(op, recErr.error, recErr.fatal, recErr.eventReason)
		}
	}

	err = r.updatePhase(op, kubocdv1alpha1.ReleasePhaseReady, true)
	if err != nil {
		return ctrl.Result{}, err // Will retry
	}

	return ctrl.Result{}, nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ReleaseReconciler) reportError(op *operation, err error, fatal bool, eventReason string) (ctrl.Result, error) {
	err2 := r.updatePhase(op, kubocdv1alpha1.ReleasePhaseError, false)
	if err2 != nil {
		return ctrl.Result{}, err // Will retry
	}
	if eventReason != "" && err != nil {
		r.Event(op.release, "Warning", eventReason, err.Error())
	}
	if fatal {
		op.logger.Error(err, "Wait for this to be fixed")
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{}, err
	}
}

func (r *ReleaseReconciler) updatePhase(op *operation, phase kubocdv1alpha1.ReleasePhase, force bool) error {
	if op.release.Status.Phase == phase && !force {
		op.logger.V(1).Info("Release phase is already up-to-date", "phase", phase)
		return nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.release.Status.Phase, "force", force)
	op.release.Status.Phase = phase
	err := r.Status().Update(op.ctx, op.release)
	//if err != nil {
	//	op.logger.Error(err, "!!!!!!!!!!Unable to update status")
	//	panic("!!!!!!!!!!Unable to update status")
	//}
	// If err != nil, will retry
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubocdv1alpha1.Release{}).
		Named("kubocd-release").
		Owns(&sourcev1b2.OCIRepository{}).
		Owns(&fluxv2.HelmRelease{}).
		Complete(r)
}
