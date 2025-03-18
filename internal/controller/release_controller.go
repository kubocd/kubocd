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
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/application"
	"kubocd/internal/cache"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"os"
	"path"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strings"
)

const OciRepositoryNameFormat = "kcd-%s"  // parameter: releaseName
const HelmRepositoryNameFormat = "kcd-%s" // parameter: releaseName
const HelmReleaseNameFormat = "kcd-%s-%s" // parameters: releaseName, moduleName

var Yes = true

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	record.EventRecorder
	Logger           logr.Logger
	Fetcher          *fetch.ArchiveFetcher
	ServerRoot       string
	HelmRepoAdvAddr  string
	ApplicationCache cache.Cache
}

// Just a container to avoid messy parameters passing
type releaseOperation struct {
	ctx                context.Context
	logger             logr.Logger
	release            *kv1alpha1.Release
	appContainer       *application.AppContainer
	ociRepositoryName  string
	helmRepositoryName string
}

// ReconcileError is a specialized error. Will allow to:
// - Specify if error is recoverable or not (fatal)
// - Specify we want to generate a Warning event.
type ReconcileError interface {
	Error() string
	IsFatal() bool
	GetEventReason() string
	GetBaseError() error
}

type reconcileErrorImpl struct {
	error       error
	fatal       bool
	eventReason string
}

var _ ReconcileError = &reconcileErrorImpl{}

func (e reconcileErrorImpl) IsFatal() bool {
	return e.fatal
}

func (e reconcileErrorImpl) GetEventReason() string {
	return e.eventReason
}

func (e reconcileErrorImpl) Error() string {
	return e.error.Error()
}

func (e reconcileErrorImpl) GetBaseError() error {
	return e.error
}

func NewReconcileError(err error, fatal bool, eventReason string) ReconcileError {
	return &reconcileErrorImpl{
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
		ociRepositoryName:  fmt.Sprintf(OciRepositoryNameFormat, release.Name),
		helmRepositoryName: fmt.Sprintf(HelmRepositoryNameFormat, release.Name),
	}

	// NB: path and folder are specific to this release.
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

	GroomRelease(release, logger)

	// ----------------------------------------------------------Setup our companion OCIRepository and wait its readiness
	ociRepository, reconcileError := r.handleOciRepository(op, global.ApplicationContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		// No need to requeue, as we should be notified when the OCI repo status will change
		return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOci, false)
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content, if not in cache
	ociArtifact := ociRepository.Status.Artifact
	revision := ociArtifact.Revision
	revisionFile := path.Join(helmRepositoryFolder, "revision.txt")

	revisionCached, err := os.ReadFile(revisionFile)
	if err != nil {
		if !errors.IsNotFound(err) {
			// Just log. Don't stop processing
			op.logger.Error(err, "Failed to read revision file")
		}
		// If notFound, it is a normal case. revision == "", so load it below
	}
	if string(revisionCached) != revision {
		err = misc.SafeEnsureEmpty(helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to clean helmRepoFolder: %w", err), true, "LocalFS"))
		}
		logger.V(1).Info("Will fetch artifact", "artifact.URL", ociArtifact.URL, "location", helmRepositoryFolder)
		err = r.Fetcher.Fetch(ociArtifact.URL, ociArtifact.Digest, helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to fetch artifact: %w", err), false, "OCIRepository"))
		}
		err = os.WriteFile(revisionFile, []byte(ociArtifact.Revision), 0644)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("writing '%s'", revisionFile), false, "LocalFS"))
		}
	} else {
		logger.V(1).Info("Use already existing application artifact")
	}
	// ----------------------------------------------------------Setup our companion HelmRepository and wait its readiness
	repoUrl := fmt.Sprintf("http://%s/%s", r.HelmRepoAdvAddr, helmRepositoryPath)
	helmRepository, reconcileError := r.handleHelmRepository(op, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		// No need to requeue, as we should be notified when the Helm repo status will change
		return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmRepo, false)
	}

	// ---------------------------------------------------------- Retrieve application from cache, or load it
	appObj := r.ApplicationCache.Get(revision)
	if appObj != nil {
		// Use value in cache
		var ok bool
		op.appContainer, ok = appObj.(*application.AppContainer)
		if !ok {
			panic("Not an appContainer in cache!")
		}
	} else {
		// Fetch application from the image
		app := &application.Application{}
		err = misc.LoadYaml(path.Join(helmRepositoryFolder, "original.yaml"), app)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing application original.yaml file: %w", err), true, "OCIImage"))
		}
		// -------- And fetch status
		status := &application.Status{}
		err = misc.LoadYaml(path.Join(helmRepositoryFolder, "status.yaml"), status)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing status.yaml file: %w", err), true, "OCIImage"))
		}
		op.appContainer = &application.AppContainer{}
		err := op.appContainer.SetApplication(app, status, revision)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while loading application from image: %w", err), true, "OCIImage"))
		}
		r.ApplicationCache.Set(revision, op.appContainer)
	}

	// ---------------------------------------------------------- Compute context
	theContext, reconcileError := ComputeContext(op.ctx, r, op.release, op.appContainer)
	if reconcileError != nil {
		return r.reportError(op, reconcileError)
	}
	err = op.appContainer.ValidateContext(theContext)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error while validating context: %w", err), false, "Context"))
	}
	// ----------------------------------------------------------------------- Handle parameters
	parameters := op.appContainer.DefaultParameters
	parameters = Merge(parameters, release.Spec.Parameters)
	err = op.appContainer.ValidateParameters(parameters)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error while validating parameters: %w", err), true, "Parameters"))
	}
	// -------------------------------------------------------------------- Render all values
	model := BuildModel(theContext, parameters, release)
	rendered, err := op.appContainer.Application.Render(model)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error on rendering: %w", err), false, "Rendering"))
	}

	// -------------------------------------------------------- Now, we are ready to spawn the helmRelease(s)
	for _, module := range op.appContainer.Application.Spec.Modules {
		helmReleaseName := fmt.Sprintf(HelmReleaseNameFormat, op.release.Name, module.Name)
		_, reconcileError := r.handleHelmRelease(op, rendered, helmReleaseName, module.Name)
		if reconcileError != nil {
			return r.reportError(op, reconcileError)
		}
		op.logger.V(1).Info("Launched helmRelease", "helmReleaseName", helmReleaseName)
	}
	forceUpdate := false
	if op.release.Spec.Debug != nil && op.release.Spec.Debug.DumpContext {
		// Sore context in status
		ba, err := json.Marshal(&theContext)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to marshal context"), false, "ContextError")) // Should not occur
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
	// TODO: store usage in status
	// TODO: Stare helmReleases in status
	return ctrl.Result{}, r.updateStatus(op, kv1alpha1.ReleasePhaseReady, forceUpdate)
}

// ComputeContext is aimed to be called by this reconciler, but also by the render CLI command
func ComputeContext(ctx context.Context, k8sClient client.Client, release *kv1alpha1.Release, appContainer *application.AppContainer) (map[string]interface{}, ReconcileError) {
	// ------ And now, build the current context
	theContext := appContainer.DefaultContext
	for _, contextNs := range release.Spec.Contexts {
		kContext := &kv1alpha1.Context{}
		err := k8sClient.Get(ctx, contextNs.ToObjectKey(), kContext)
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
		theContext = Merge(theContext, ctx)
	}
	return theContext, nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ReleaseReconciler) reportError(op *releaseOperation, rErr ReconcileError) (ctrl.Result, error) {
	err2 := r.updateStatus(op, kv1alpha1.ReleasePhaseError, false)
	if err2 != nil {
		return ctrl.Result{}, rErr // Will retry
	}
	if rErr.GetEventReason() != "" && rErr.GetBaseError() != nil {
		r.Event(op.release, "Warning", rErr.GetEventReason(), rErr.Error())
	}
	if rErr.IsFatal() {
		op.logger.Error(rErr, "Wait for this to be fixed")
		return ctrl.Result{}, nil
	} else {
		return ctrl.Result{}, rErr
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

// GroomRelease is aimed to be called by this reconciler, but also by the render CLI command
func GroomRelease(release *kv1alpha1.Release, logger logr.Logger) {
	if release.Spec.Contexts == nil {
		release.Spec.Contexts = make([]kv1alpha1.NamespacedName, 0)
	}
	if release.Spec.Roles == nil {
		release.Spec.Roles = make([]string, 0)
	}
	if release.Spec.DependsOn == nil {
		release.Spec.DependsOn = make([]string, 0)
	}
	if release.Spec.Debug == nil {
		release.Spec.Debug = &kv1alpha1.ReleaseDebug{}
	}
	for i := range release.Spec.Contexts {
		kctx := &release.Spec.Contexts[i]
		if kctx.Namespace == "" {
			logger.V(1).Info("Set namespace for context", "contextName", kctx.Name, "contextNamespace", release.ObjectMeta.Namespace)
			kctx.Namespace = release.ObjectMeta.Namespace
		}
	}
}

func BuildModel(context map[string]interface{}, parameters map[string]interface{}, release *kv1alpha1.Release) map[string]interface{} {
	model := map[string]interface{}{
		"Context":    context,
		"Parameters": parameters,
		"Release":    misc.ObjectToMap(release),
	}
	return model
}
