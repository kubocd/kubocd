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
	"errors"
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/http/fetch"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/service"
	"os"
	"path"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ociRepositoryNameFormat = "kcd-%s"  // parameter: releaseName
const helmRepositoryNameFormat = "kcd-%s" // parameter: releaseName
const helmReleaseNameFormat = "kcd-%s-%s" // parameters: releaseName, moduleName

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	record.EventRecorder
	Logger          logr.Logger
	Fetcher         *fetch.ArchiveFetcher
	ServerRoot      string
	HelmRepoAdvAddr string
}

// Just a container to avoid messy parameters passing
type operation struct {
	ctx                context.Context
	logger             logr.Logger
	release            *kubocdv1alpha1.Release
	service            *service.Service
	ociRepositoryName  string
	helmRepositoryName string
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
		ctx:                ctx,
		logger:             logger,
		release:            release,
		ociRepositoryName:  fmt.Sprintf(ociRepositoryNameFormat, release.Name),
		helmRepositoryName: fmt.Sprintf(helmRepositoryNameFormat, release.Name),
	}

	helmRepositoryPath := path.Join("hr", op.release.Namespace, op.release.Name)
	helmRepositoryFolder := path.Join(r.ServerRoot, helmRepositoryPath)

	if !release.ObjectMeta.DeletionTimestamp.IsZero() {
		// Deletion is requested
		if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
			// No finalizer at all. Nothing to do anymore
			return ctrl.Result{}, nil
		}
		logger.V(1).Info("Deleting release")
		// Perform deletion cleanup.
		err := misc.SafeRemove(helmRepositoryFolder)
		if err != nil {
			// Just log, without any other action
			op.logger.Error(err, "Failed to remove helm repository folder '%s'", helmRepositoryFolder)
		}
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
	// ----------------------------------------------------------Setup our companion OCIRepository and wait its readiness
	ociRepository, reconcileError := r.handleOciRepository(op, global.ServiceContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		err = r.updatePhase(op, kubocdv1alpha1.ReleasePhaseWaitOci, false)
		if err != nil {
			return ctrl.Result{}, err // Will retry
		}
		// No need to requeue, as we should be notified when the OCI repo status will change
		//return ctrl.Result{RequeueAfter: time.Millisecond * 1000}, nil
		return ctrl.Result{}, nil
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content
	ociArtifact := ociRepository.Status.Artifact

	revisionFile := path.Join(helmRepositoryFolder, "revision.txt")

	revision, err := os.ReadFile(revisionFile)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			// Just log. Don't stop processing
			op.logger.Error(err, "Failed to read revision file")
		}
	}
	if string(revision) != ociArtifact.Revision {
		err = misc.SafeEnsureEmpty(helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, fmt.Errorf("unable to clean helmRepoFolder: %w", err), true, "LocalFS")
		}
		err = os.WriteFile(revisionFile, []byte(ociArtifact.Revision), 0644)
		if err != nil {
			return r.reportError(op, fmt.Errorf("writing '%s'", revisionFile), false, "LocalFS")
		}

		logger.V(1).Info("Will fetch artifact", "artifact.URL", ociArtifact.URL, "location", helmRepositoryFolder)
		err = r.Fetcher.Fetch(ociArtifact.URL, ociArtifact.Digest, helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, fmt.Errorf("unable to fetch artifact: %w", err), false, "OCIRepository")
		}
	} else {
		logger.V(1).Info("Use already existing artifact")
	}
	// Set Service object
	op.service = &service.Service{}
	err = misc.LoadYaml(path.Join(helmRepositoryFolder, "manifest.yaml"), op.service)
	if err != nil {
		return r.reportError(op, fmt.Errorf("error while parsing Manifest.yaml file: %w", err), true, "OCIImage")
	}
	//fmt.Printf("Manifest: %s\n", misc.Map2Yaml(op.service))

	// ----------------------------------------------------------Setup our companion HelmRepository and wait its readiness
	repoUrl := fmt.Sprintf("http://%s/%s", r.HelmRepoAdvAddr, helmRepositoryPath)
	helmRepository, reconcileError := r.handleHelmRepository(op, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		err = r.updatePhase(op, kubocdv1alpha1.ReleasePhaseWaitHelmRepo, false)
		if err != nil {
			return ctrl.Result{}, err // Will retry
		}
		// No need to requeue, as we should be notified when the Helm repo status will change
		//return ctrl.Result{RequeueAfter: time.Millisecond * 1000}, nil
		return ctrl.Result{}, nil
	}
	// -------------------------------------------------------- Now, we are ready to spawn the helmRelease(s)
	for module := range op.service.Status.ChartByModule {
		helmReleaseName := fmt.Sprintf(helmReleaseNameFormat, op.release.Name, module)
		_, reconcileError := r.handleHelmRelease(op, helmReleaseName, module)
		if reconcileError != nil {
			return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
		}
		op.logger.V(1).Info("Launched helmRelease", "helmReleaseName", helmReleaseName)
	}

	//// ---------------------------------------------- Spawn secondary ociRepo
	//for _, chart := range srv.Status.Charts {
	//	fmt.Printf("Chart %s\n", misc.Map2Yaml(chart))
	//	ociRepoName := fmt.Sprintf("%s-%s", op.release.Name, chart.Module)
	//	_, reconcileError := r.handleOciRepository(op, ociRepoName, chart.MediaType, "copy")
	//	if reconcileError != nil {
	//		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	//	}
	//}
	//// --------------------------------------------- And spawn helmReleases
	//for _, chart := range srv.Status.Charts {
	//	_, recErr := r.handleHelmRelease(op, chart.Module)
	//	if recErr != nil {
	//		return r.reportError(op, recErr.error, recErr.fatal, recErr.eventReason)
	//	}
	//}

	err = r.updatePhase(op, kubocdv1alpha1.ReleasePhaseReady, false)
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

func buildConditionStatusByType(conditions []metav1.Condition, repoKind string, repoName string, logger logr.Logger) map[string]metav1.ConditionStatus {
	statusByType := make(map[string]metav1.ConditionStatus)
	if len(conditions) < 2 {
		logger.V(0).Info("Not enough conditions found yet", repoKind, repoName)
	}
	for _, condition := range conditions {
		logger.V(1).Info("condition", "type", condition.Type, "status", condition.Status, repoKind, repoName)
		statusByType[condition.Type] = condition.Status
	}
	return statusByType
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubocdv1alpha1.Release{}).
		Named("kubocd-release").
		Owns(&sourcev1b2.OCIRepository{}).
		Owns(&sourcev1.HelmRepository{}).
		Owns(&fluxv2.HelmRelease{}).
		Complete(r)
}
