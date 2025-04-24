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
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/rolestore"
	"kubocd/internal/tmpl"
	"os"
	"path"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"strconv"
	"strings"
	"time"
)

const OciRepositoryNameFormat = "kcd-%s"  // parameter: releaseName
const HelmRepositoryNameFormat = "kcd-%s" // parameter: releaseName
const HelmReleaseNameFormat = "kcd-%s-%s" // parameters: releaseName, moduleName

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	record.EventRecorder
	Logger           logr.Logger
	Fetcher          *fetch.ArchiveFetcher
	ServerRoot       string
	HelmRepoAdvAddr  string
	PackageCache     cache.Cache
	ConfigStore      configstore.ConfigStore
	RoleStore        rolestore.RoleStore
	statusErrorCount int
}

// Just a container to avoid messy parameters passing
type releaseOperation struct {
	request                     ctrl.Request
	ctx                         context.Context
	logger                      logr.Logger
	release                     *kv1alpha1.Release
	pckContainer                *kubopackage.PckContainer
	ociRepositoryName           string
	helmRepositoryName          string
	helmReleaseStates           map[string]kv1alpha1.HelmReleaseState // To collect values
	helmReleaseNameByModuleName map[string]string
	roles                       []string
	dependencies                []string
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
		request:            req,
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
		r.RoleStore.UnRegisterRelease(req.NamespacedName)
		if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
			// No finalizer at all. Nothing to do anymore
			return ctrl.Result{}, nil
		}
		logger.V(0).Info("Deleting release")
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
	// As we want Status to be explicit about provided information, we don't use 'omitempty' in its definition.
	// This means we must set some empty default value, otherwise status write will fail
	if release.Status.HelmReleaseStates == nil {
		release.Status.HelmReleaseStates = make(map[string]kv1alpha1.HelmReleaseState)
	}
	if release.Status.Dependencies == nil {
		release.Status.Dependencies = make([]string, 0)
	}
	if release.Status.Roles == nil {
		release.Status.Roles = make([]string, 0)
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
	ociRepository, reconcileError := r.handleOciRepository(op, global.PackageContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		// No need to requeue, as we should be notified when the OCI repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOci, false)
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content, if not in cache
	ociArtifact := ociRepository.Status.Artifact
	revision := ociArtifact.Revision
	revisionFile := path.Join(helmRepositoryFolder, "revision.txt")

	revisionCached, err := os.ReadFile(revisionFile)
	if err != nil {
		if !os.IsNotExist(err) {
			op.logger.Error(err, "Failed to read revision file")
		}
		// If notFound, it is a normal case. revision == "", so load it below
	}
	if string(revisionCached) != revision {
		err = misc.SafeEnsureEmpty(helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to clean helmRepoFolder: %w", err), true, "LocalFS"), false)
		}
		logger.V(1).Info("Will fetch artifact", "artifact.URL", ociArtifact.URL, "location", helmRepositoryFolder)
		err = r.Fetcher.Fetch(ociArtifact.URL, ociArtifact.Digest, helmRepositoryFolder)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to fetch artifact: %w", err), false, "OCIRepository"), false)
		}
		err = os.WriteFile(revisionFile, []byte(ociArtifact.Revision), 0644)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("writing '%s'", revisionFile), false, "LocalFS"), false)
		}
	} else {
		logger.V(1).Info("Use already existing cached package artifact")
	}
	// ----------------------------------------------------------Setup our companion HelmRepository and wait its readiness
	repoUrl := fmt.Sprintf("http://%s/%s", r.HelmRepoAdvAddr, helmRepositoryPath)
	helmRepository, reconcileError := r.handleHelmRepository(op, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		// No need to requeue, as we should be notified when the Helm repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmRepo, false)
	}

	// ---------------------------------------------------------- Retrieve package from cache, or load it
	pckObj := r.PackageCache.Get(revision)
	if pckObj != nil {
		// Use value in cache
		var ok bool
		op.pckContainer, ok = pckObj.(*kubopackage.PckContainer)
		if !ok {
			panic("Not an pckContainer in cache!")
		}
	} else {
		// Fetch Package from the image
		pck := &kubopackage.Package{}
		err = misc.LoadYaml(path.Join(helmRepositoryFolder, "original.yaml"), pck)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing package original.yaml file: %w", err), true, "OCIImage"), false)
		}
		// -------- And fetch status
		status := &kubopackage.Status{}
		err = misc.LoadYaml(path.Join(helmRepositoryFolder, "status.yaml"), status)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing status.yaml file: %w", err), true, "OCIImage"), false)
		}
		op.pckContainer = &kubopackage.PckContainer{}
		err := op.pckContainer.SetPackage(pck, status, revision)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while loading package from image: %w", err), true, "OCIImage"), false)
		}
		r.PackageCache.Set(revision, op.pckContainer)
	}

	forceUpdate := false
	// Store protected in status
	protected := op.pckContainer.Package.Protected
	if op.release.Spec.Protected != nil {
		protected = *op.release.Spec.Protected
	}
	op.release.Status.PrintProtected = misc.Ternary(protected, "X", "-")
	if protected != op.release.Status.Protected {
		op.release.Status.Protected = protected
		forceUpdate = true
	}
	// ---------------------------------------------------------- Compute context
	theContext, contextList, reconcileError := ComputeContext(op.ctx, r, op.release, r.ConfigStore, op.pckContainer.DefaultContext)
	if reconcileError != nil {
		return r.reportError(op, reconcileError, forceUpdate)
	}
	ctxList := misc.FlattenNamespacedNames(contextList)
	if ctxList != op.release.Status.PrintContexts {
		op.release.Status.PrintContexts = ctxList
		forceUpdate = true
	}
	if op.release.Spec.Debug != nil {
		if op.release.Spec.Debug.DumpContext {
			// Sore context in status
			ba, err := json.Marshal(&theContext)
			if err != nil {
				return r.reportError(op, NewReconcileError(fmt.Errorf("unable to marshal context"), false, "ContextError"), forceUpdate) // Should not occur
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
	}
	// ------------------------------------------------------ Validate context
	err = op.pckContainer.ValidateContext(theContext)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error while validating context: %w", err), true, "Context"), forceUpdate)
	}
	// ----------------------------------------------------------------------- Handle parameters
	parameters, err := HandleParameters(release, theContext, r.ConfigStore, op.pckContainer)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, true, "Parameters"), forceUpdate)
	}
	if op.release.Spec.Debug != nil {
		if op.release.Spec.Debug.DumpParameters {
			// Sore parameters in status
			ba, err := json.Marshal(&parameters)
			if err != nil {
				return r.reportError(op, NewReconcileError(fmt.Errorf("unable to marshal parameters"), false, "ParametersError"), forceUpdate) // Should not occur
			}
			op.release.Status.Parameters = &apiextensionsv1.JSON{
				Raw: ba,
			}
			forceUpdate = true
		} else {
			if op.release.Status.Parameters != nil {
				op.release.Status.Parameters = nil
				forceUpdate = true
			}
		}
	}
	// -------------------------------------------------------------------- Render all values
	model := BuildModel(theContext, parameters, release, r.ConfigStore)
	rendered, err := op.pckContainer.Package.Render(model)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error on rendering: %w", err), false, "Rendering"), forceUpdate)
	}
	// --------------------------------------------------- Store some rendered values to status
	if !reflect.DeepEqual(rendered.Usage, op.release.Status.Usage) {
		op.release.Status.Usage = rendered.Usage
		forceUpdate = true
	}
	description := op.release.Spec.Description
	if description == "" {
		description = rendered.Description
	}
	if description != op.release.Status.PrintDescription {
		op.release.Status.PrintDescription = description
		forceUpdate = true
	}

	// --------------------------------------------------------------------- compute roles/dependencies
	// Roles will be registered at the end, only if status is READY
	op.roles = misc.RemoveDuplicates(append(rendered.Roles, release.Spec.Roles...))
	op.dependencies = misc.RemoveDuplicates(append(rendered.Dependencies, release.Spec.Dependencies...))
	if !reflect.DeepEqual(op.roles, op.release.Status.Roles) {
		op.release.Status.Roles = op.roles
		forceUpdate = true
	}
	if !reflect.DeepEqual(op.dependencies, op.release.Status.Dependencies) {
		op.release.Status.Dependencies = op.dependencies
		forceUpdate = true
	}

	// --------------------------------------- Build a map of module by name for intra-package dependencies handling.
	op.helmReleaseNameByModuleName = make(map[string]string)
	for _, module := range op.pckContainer.Package.Modules {
		op.helmReleaseNameByModuleName[module.Name] = BuildHelmReleaseName(op.release.Name, module.Name)
	}

	// ------------------------------------------------------------------Prepare status update

	// ---------------------------------------------------------- Test if our dependencies are OK. If not, set status and loop back after 5s
	missing := r.RoleStore.MissingDependency(req.NamespacedName, op.dependencies)
	if missing != op.release.Status.MissingDependency {
		op.release.Status.MissingDependency = missing
		forceUpdate = true
	}
	if missing != "" {
		r.Event(op.release, "Normal", "MissingDependency", fmt.Sprintf("Waiting for the role '%s' to be ready", missing))
		r, err := r.updateStatus(op, kv1alpha1.ReleasePhaseWaitDependencies, forceUpdate)
		if err != nil {
			return ctrl.Result{}, err
		}
		if r.Requeue {
			// It is a Requeue due to update status error
			return r, nil
		}
		return ctrl.Result{
			RequeueAfter: time.Second * 5,
		}, nil
	}

	// -------------------------------------------------------- Now, we are ready to spawn the helmRelease(s)
	if !op.release.Spec.Suspended {
		op.helmReleaseStates = make(map[string]kv1alpha1.HelmReleaseState)
		for _, module := range op.pckContainer.Package.Modules {
			helmReleaseName := BuildHelmReleaseName(op.release.Name, module.Name)
			_, reconcileError := r.handleHelmRelease(op, rendered, helmReleaseName, module)
			if reconcileError != nil {
				return r.reportError(op, reconcileError, forceUpdate)
			}
		}
	}
	// -------------------------------------------------------- Adjust status
	// And store helmReleases status
	readyReleases, allReady := computeReadyReleases(op)
	if readyReleases != op.release.Status.ReadyReleases {
		op.release.Status.ReadyReleases = readyReleases
		forceUpdate = true
	}
	if !reflect.DeepEqual(op.helmReleaseStates, op.release.Status.HelmReleaseStates) {
		op.release.Status.HelmReleaseStates = op.helmReleaseStates
		forceUpdate = true
	}
	var phase kv1alpha1.ReleasePhase
	if op.release.Spec.Suspended {
		phase = kv1alpha1.ReleasePhaseSuspended
	} else {
		if allReady {
			phase = kv1alpha1.ReleasePhaseReady
		} else {
			phase = kv1alpha1.ReleasePhaseWaitHelmReleases
		}
	}
	return r.updateStatus(op, phase, forceUpdate)
}

func HandleParameters(release *kv1alpha1.Release, kcontext map[string]interface{}, configStore configstore.ConfigStore, pckContainer *kubopackage.PckContainer) (map[string]interface{}, error) {

	var parametersStr string
	if release.Spec.Parameters == nil || release.Spec.Parameters.Raw == nil || len(release.Spec.Parameters.Raw) == 0 {
		parametersStr = "{}"
		//return pckContainer.DefaultParameters, nil
	} else {
		parametersStr = string(release.Spec.Parameters.Raw)
	}

	var err error
	if strings.Contains(parametersStr, "\\n") && parametersStr[0:1] != "{" { // If there is some '\n' and this is not json.
		parametersStr, err = strconv.Unquote(parametersStr)
		if err != nil {
			return nil, fmt.Errorf("could not unquote parameter value: %w", err)
		}
	}

	parametersTmpl, err := tmpl.NewFromAny("", parametersStr, "")
	if err != nil {
		return nil, fmt.Errorf("could not create template from parameters: %w", err)
	}
	pModel := BuildModel(kcontext, nil, release, configStore)

	parameters, txt, err := parametersTmpl.RenderToMap(pModel)
	if err != nil {
		return nil, fmt.Errorf("could not render parameters template: %w (%s)", err, txt)
	}

	parameters = misc.MergeMaps(pckContainer.DefaultParameters, parameters)
	err = pckContainer.ValidateParameters(parameters)
	if err != nil {
		return nil, fmt.Errorf("could not validate parameters: %w", err)
	}
	return parameters, nil
}

func BuildHelmReleaseName(releaseName, moduleName string) string {
	return fmt.Sprintf(HelmReleaseNameFormat, releaseName, moduleName)
}

func computeReadyReleases(op *releaseOperation) (str string, allReady bool) {
	cnt := 0
	for _, releaseState := range op.helmReleaseStates {
		if releaseState.Ready == metav1.ConditionTrue {
			cnt++
		}
	}
	return fmt.Sprintf("%d/%d", cnt, len(op.helmReleaseStates)), cnt == len(op.helmReleaseStates)
}

// ComputeContext is aimed to be called by this reconciler, but also by the render CLI command
func ComputeContext(ctx context.Context, k8sClient client.Client, release *kv1alpha1.Release, store configstore.ConfigStore, defaultContext map[string]interface{}) (map[string]interface{}, []kv1alpha1.NamespacedName, ReconcileError) {

	optionalContexts := make(map[kv1alpha1.NamespacedName]bool)

	contextList := make([]kv1alpha1.NamespacedName, 0, 3)
	if !release.Spec.SkipDefaultContext {
		contextList = append(contextList, store.GetDefaultContexts()...)
		for _, nsContextName := range store.GetDefaultNamespaceContexts() {
			nsContext := kv1alpha1.NamespacedName{
				Namespace: release.GetNamespace(),
				Name:      nsContextName,
			}
			contextList = append(contextList, nsContext)
			optionalContexts[nsContext] = true
		}
	}
	contextList = append(contextList, release.Spec.Contexts...)
	effectiveContextList := make([]kv1alpha1.NamespacedName, 0, len(contextList))
	resultContext := defaultContext
	for _, contextRef := range contextList {
		contextObj := &kv1alpha1.Context{}
		err := k8sClient.Get(ctx, contextRef.ToObjectKey(), contextObj)
		if err != nil {
			if k8serror.IsNotFound(err) {
				if optionalContexts[contextRef] {
					continue // This specific context may not exist. This is not an error
				} else {
					return nil, nil, NewReconcileError(fmt.Errorf("context '%s' not found", contextRef.String()), true, "ContextNotFound")
				}
			} else {
				return nil, nil, NewReconcileError(err, false, "ContextRetrieval")
			}
		}
		if contextObj.Status.Phase != kv1alpha1.ContextPhaseReady {
			return nil, nil, NewReconcileError(fmt.Errorf(fmt.Sprintf("Context '%s' is in error", contextRef.String())), true, "ContextRetrieval")
		}
		// OK. Merge our info on top
		ctx := contextObj.Status.Context
		if ctx == nil {
			ctx = contextObj.Spec.Context
		}
		resultContext, err = Merge(resultContext, ctx)
		if err != nil {
			return nil, nil, NewReconcileError(fmt.Errorf("unable to merge context: %w", err), true, "ContextMerge")
		}
		effectiveContextList = append(effectiveContextList, contextRef)
	}
	return resultContext, effectiveContextList, nil
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ReleaseReconciler) reportError(op *releaseOperation, rErr ReconcileError, forceUpdate bool) (ctrl.Result, error) {
	ctrlResult, err2 := r.updateStatus(op, kv1alpha1.ReleasePhaseError, forceUpdate)
	if err2 != nil {
		return ctrl.Result{}, rErr // Will retry
	}
	if rErr.GetEventReason() != "" && rErr.GetBaseError() != nil {
		r.Event(op.release, "Warning", rErr.GetEventReason(), rErr.Error())
	}
	if rErr.IsFatal() {
		op.logger.Error(rErr, "Wait for this to be fixed")
		return ctrlResult, nil
	} else {
		return ctrl.Result{}, rErr
	}
}

func (r *ReleaseReconciler) updateStatus(op *releaseOperation, phase kv1alpha1.ReleasePhase, force bool) (ctrl.Result, error) {
	if phase == kv1alpha1.ReleasePhaseReady {
		r.RoleStore.RegisterRelease(op.request.NamespacedName, op.roles)
	} else {
		r.RoleStore.UnRegisterRelease(op.request.NamespacedName)
	}
	if op.release.Status.Phase == phase && !force {
		op.logger.V(1).Info("Release phase is already up-to-date", "phase", phase)
		//fmt.Printf("  .  .  .   .   .   .   : %s\n", phase)
		return ctrl.Result{}, nil
	}
	op.logger.V(1).Info("Updating phase", "newPhase", phase, "oldPhase", op.release.Status.Phase, "force", force)
	op.release.Status.Phase = phase
	err := r.Status().Update(op.ctx, op.release)
	if err != nil {
		//fmt.Printf("***********************: %s    (%T)\n", phase, err)
		if r.statusErrorCount > 0 {
			return ctrl.Result{}, err
		} else {
			r.statusErrorCount++
			op.logger.V(1).Info("Error updating status. Hidden as first one", "phase", phase)
			return ctrl.Result{Requeue: true}, nil
		}
	} else {
		//fmt.Printf("-----------------------: %s\n", phase)
		r.statusErrorCount = 0
		return ctrl.Result{}, err
	}
}

func buildConditionStatusByType(conditions []metav1.Condition, repoKind string, repoName string, logger logr.Logger) map[string]metav1.ConditionStatus {
	statusByType := make(map[string]metav1.ConditionStatus)
	if len(conditions) < 2 {
		logger.V(1).Info("Not enough conditions found yet", repoKind, repoName)
	}
	for _, condition := range conditions {
		logger.V(1).Info("condition", "type", condition.Type, "status", condition.Status, repoKind, repoName)
		statusByType[condition.Type] = condition.Status
	}
	return statusByType
}

// GroomRelease is aimed to be called by this reconciler, but also by the render CLI command
func GroomRelease(release *kv1alpha1.Release, logger logr.Logger) {
	if release.Spec.TargetNamespace == "" {
		release.Spec.TargetNamespace = release.Namespace
	}
	if release.Spec.Contexts == nil {
		release.Spec.Contexts = make([]kv1alpha1.NamespacedName, 0)
	}
	if release.Spec.Roles == nil {
		release.Spec.Roles = make([]string, 0)
	}
	if release.Spec.Dependencies == nil {
		release.Spec.Dependencies = make([]string, 0)
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

func BuildModel(context map[string]interface{}, parameters map[string]interface{}, release *kv1alpha1.Release, store configstore.ConfigStore) map[string]interface{} {
	model := map[string]interface{}{
		"Context":         context,
		"Parameters":      parameters,
		"Release":         misc.ObjectToMap(release),
		"ImageRedirector": store,
	}
	return model
}
