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
	"github.com/fluxcd/pkg/http/fetch"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/application"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"os"
	"path"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"
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
type releaseOperation struct {
	ctx                context.Context
	logger             logr.Logger
	release            *kv1alpha1.Release
	application        *application.Application
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
	release := &kv1alpha1.Release{}
	err := r.Get(ctx, req.NamespacedName, release)
	if err != nil {
		logger.V(1).Info("Unable to fetch resource. Seems deleted")
		// we'll ignore not-found errors, since they can't be fixed by an immediate requeue
		// (we'll need to wait for a new notification), and we can get them on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	op := &releaseOperation{
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

	rErr := groomRelease(release, logger)
	if rErr != nil {
		return r.reportError(op, rErr.error, rErr.fatal, rErr.eventReason)
	}

	// ----------------------------------------------------------Setup our companion OCIRepository and wait its readiness
	ociRepository, reconcileError := r.handleOciRepository(op, global.ApplicationContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		// No need to requeue, as we should be notified when the OCI repo status will change
		return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOci, false)
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content
	ociArtifact := ociRepository.Status.Artifact

	revisionFile := path.Join(helmRepositoryFolder, "revision.txt")

	revision, err := os.ReadFile(revisionFile)
	if err != nil {
		if !errors.IsNotFound(err) {
			// Just log. Don't stop processing
			op.logger.Error(err, "Failed to read revision file")
		}
	}
	if string(revision) != ociArtifact.Revision {
		err = misc.SafeEnsureEmpty(helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, fmt.Errorf("unable to clean helmRepoFolder: %w", err), true, "LocalFS")
		}
		logger.V(1).Info("Will fetch artifact", "artifact.URL", ociArtifact.URL, "location", helmRepositoryFolder)
		err = r.Fetcher.Fetch(ociArtifact.URL, ociArtifact.Digest, helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, fmt.Errorf("unable to fetch artifact: %w", err), false, "OCIRepository")
		}
		err = os.WriteFile(revisionFile, []byte(ociArtifact.Revision), 0644)
		if err != nil {
			return r.reportError(op, fmt.Errorf("writing '%s'", revisionFile), false, "LocalFS")
		}
	} else {
		logger.V(1).Info("Use already existing artifact")
	}
	// Set Application object
	op.application = &application.Application{}
	err = misc.LoadYaml(path.Join(helmRepositoryFolder, "manifest.yaml"), op.application)
	if err != nil {
		return r.reportError(op, fmt.Errorf("error while parsing Manifest.yaml file: %w", err), true, "OCIImage")
	}
	//fmt.Printf("Manifest: %s\n", misc.Map2Yaml(op.application))

	// ----------------------------------------------------------Setup our companion HelmRepository and wait its readiness
	repoUrl := fmt.Sprintf("http://%s/%s", r.HelmRepoAdvAddr, helmRepositoryPath)
	helmRepository, reconcileError := r.handleHelmRepository(op, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		// No need to requeue, as we should be notified when the Helm repo status will change
		return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmRepo, false)
	}
	// ---------------------------------------------------------- Compute context, and store in status, if requested
	theContext, reconcileError := r.computeContext(op)
	if reconcileError != nil {
		return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
	}
	// -------------------------------------------------------- Now, we are ready to spawn the helmRelease(s)
	for module := range op.application.Status.ChartByModule {
		helmReleaseName := fmt.Sprintf(helmReleaseNameFormat, op.release.Name, module)
		_, reconcileError := r.handleHelmRelease(op, helmReleaseName, module)
		if reconcileError != nil {
			return r.reportError(op, reconcileError.error, reconcileError.fatal, reconcileError.eventReason)
		}
		op.logger.V(1).Info("Launched helmRelease", "helmReleaseName", helmReleaseName)
	}
	forceUpdate := false
	if op.release.Spec.Debug != nil && op.release.Spec.Debug.DumpContext {
		// Sore in status
		ba, err := json.Marshal(&theContext)
		if err != nil {
			return r.reportError(op, fmt.Errorf("unable to marshal context"), false, "ContextError") // Should not occur
		}
		op.release.Status.Context = &apiextensionsv1.JSON{
			Raw: ba,
		}
		forceUpdate = true
	} else {
		if op.release.Status.Context != nil {
			op.release.Status.Context = nil
			forceUpdate = true
		}
	}

	return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseReady, forceUpdate)
}

func (r *ReleaseReconciler) computeContext(op *releaseOperation) (map[string]interface{}, *ReconcileError) {
	// ------ And now, build the current context
	theContext := make(map[string]interface{})
	for _, contextNs := range op.release.Spec.Contexts {
		kContext := &kv1alpha1.Context{}
		err := r.Get(op.ctx, contextNs.ToObjectKey(), kContext)
		if err != nil {
			if errors.IsNotFound(err) {
				return nil, NewReconcileError(err, true, "ContextNotFound")
			} else {
				return nil, NewReconcileError(err, false, "ContextRetrieval")
			}
		}
		if kContext.Status.Phase != kv1alpha1.ContextPhaseReady {
			return nil, NewReconcileError(fmt.Errorf(fmt.Sprintf("Context '%s' is in error", contextNs.String())), true, "ContextRetrieval")
		}
		// OK. Merge our info on top of our parent
		ctx := kContext.Status.Context
		if ctx == nil {
			ctx = kContext.Spec.Context
		}
		theContext, err = merge(theContext, ctx)
		if err != nil {
			return nil, NewReconcileError(fmt.Errorf(fmt.Sprintf("Context '%s' is in error", contextNs.String())), true, "ContextRetrieval")
		}
	}
	return theContext, nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ReleaseReconciler) reportError(op *releaseOperation, err error, fatal bool, eventReason string) (ctrl.Result, error) {
	err2 := r.updateStatus(op, kv1alpha1.ReleasePhaseError, false)
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

func buildContextsList(release *kv1alpha1.Release) string {
	if len(release.Spec.Contexts) == 0 {
		return ""
	}
	contexts := make([]string, len(release.Spec.Contexts))
	for idx := range release.Spec.Contexts {
		contexts[idx] = release.Spec.Contexts[idx].String()
	}
	return strings.Join(contexts, ",")
}

func (r *ReleaseReconciler) updateStatus(op *releaseOperation, phase kv1alpha1.ReleasePhase, force bool) error {
	ctxs := buildContextsList(op.release)
	if op.release.Status.Phase == phase && op.release.Status.Contexts == ctxs && !force {
		op.logger.V(1).Info("Release phase is already up-to-date", "phase", phase)
		return nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.release.Status.Phase, "force", force)
	op.release.Status.Phase = phase
	op.release.Status.Contexts = ctxs
	err := r.Status().Update(op.ctx, op.release)
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

func groomRelease(release *kv1alpha1.Release, logger logr.Logger) *ReconcileError {
	for i := range release.Spec.Contexts {
		kctx := &release.Spec.Contexts[i]
		if kctx.Namespace == "" {
			logger.V(1).Info("Set namespace for context", "contextName", kctx.Name, "contextNamespace", release.ObjectMeta.Namespace)
			kctx.Namespace = release.ObjectMeta.Namespace
		}
	}
	return nil
}
