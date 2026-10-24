/*
Copyright 2025 Kubotal

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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/rolestore"
	"kubocd/internal/tmpl"
	"maps"
	"os"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const OciRepositoryNameFormat = "kcd-%s"  // parameter: releaseName
const HelmRepositoryNameFormat = "kcd-%s" // parameter: releaseName
const HelmReleaseNameFormat = "%s-%s"     // parameters: releaseName, moduleName

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	record.EventRecorder
	Logger          logr.Logger
	Fetcher         *PackageFetcher
	ServerRoot      string
	HelmRepoAdvAddr string
	PackageCache    cache.Cache
	ConfigStore     configstore.ConfigStore
	RoleStore       rolestore.RoleStore
	AllowDNSLookups bool
	ConnectionsGate bool // On when the controllers that check connections run
	statusFailures  statusFailures
}

// statusFailures holds the Releases whose last status update failed.
type statusFailures struct {
	mutex    sync.Mutex
	releases map[types.NamespacedName]bool
}

// fail records a failed status update of the Release, and tells whether its
// previous one failed too.
func (s *statusFailures) fail(release types.NamespacedName) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if s.releases == nil {
		s.releases = make(map[types.NamespacedName]bool)
	}
	again := s.releases[release]
	s.releases[release] = true
	return again
}

func (s *statusFailures) clear(release types.NamespacedName) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	delete(s.releases, release)
}

var _ reconcile.Reconciler = &ReleaseReconciler{}
var _ BuildInputModelHelper = &ReleaseReconciler{}

// Just a container to avoid messy parameters passing
type releaseOperation struct {
	request                        ctrl.Request
	ctx                            context.Context
	logger                         logr.Logger
	release                        *kv1alpha1.Release
	pckContainer                   *kubopackage.PckContainer
	ociRepositoryName              string
	helmRepositoryName             string
	helmReleaseStates              map[string]kv1alpha1.HelmReleaseState        // To collect values for user display
	outputConnectionByName         map[string]kv1alpha1.ReleaseOutputConnection // To collect values for user display and index by connection. Host both Connection and ClusterConnection
	outputConnectionK8sName        map[string]struct{}                          // To prevent orphan deletion
	outputClusterConnectionK8sName map[string]struct{}                          // To prevent orphan deletion
	helmReleaseNameByModuleName    map[string]string
	roles                          []string
	dependencies                   []string
}

// ReconcileError is a specialized error. Will allow to:
// - Specify if error is recoverable or not (fatal). If fatal, there will be no retry (return ctrlResult, nil)
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

// Behave like a standard error
func (e reconcileErrorImpl) Error() string {
	return e.error.Error()
}

func (e reconcileErrorImpl) GetBaseError() error {
	return e.error
}

// NewReconcileError Generate a specialized error.
// - fatal: Error is on our own. No need to retry
// - eventReason != "" -> Generate an event
func NewReconcileError(err error, fatal bool, eventReason string) ReconcileError {
	return &reconcileErrorImpl{
		error:       err,
		fatal:       fatal,
		eventReason: eventReason,
	}
}

// atPlace returns rErr with place, such as output[0], before its message.
func atPlace(place string, rErr ReconcileError) ReconcileError {
	return NewReconcileError(fmt.Errorf("%s: %w", place, rErr.GetBaseError()), rErr.IsFatal(), rErr.GetEventReason())
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
		r.statusFailures.clear(req.NamespacedName)
		if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
			// No finalizer at all. Nothing to do anymore
			return ctrl.Result{}, nil
		}
		logger.V(0).Info("Deleting release")
		// Perform deletion cleanup.
		err := misc.SafeRemove(helmRepositoryFolder)
		if err != nil {
			// Just log, without any other action
			op.logger.Error(err, "Failed to remove helm repository folder", "folder", helmRepositoryFolder)
		}
		// The ClusterConnections of the Release have no owner reference: the finalizer is what removes them
		if err := r.deleteOutputClusterConnections(op); err != nil {
			return ctrl.Result{}, err
		}
		// Deletion OK
		controllerutil.RemoveFinalizer(release, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Remove finalizer)")
		if err := r.Update(ctx, release); err != nil {
			return ctrl.Result{}, fmt.Errorf("could not remove the finalizer of the Release: %s", apiError(err))
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
	if release.Status.WatchedInputConnections == nil {
		release.Status.WatchedInputConnections = make([]kv1alpha1.InputConnectionReference, 0)
	}
	if release.Status.EffectiveInputConnections == nil {
		release.Status.EffectiveInputConnections = make([]kv1alpha1.InputConnectionReference, 0)
	}

	// Not under deletion. Add a finalizer if not already set
	if !controllerutil.ContainsFinalizer(release, global.FinalizerName) {
		logger.V(1).Info("Add finalizer")
		controllerutil.AddFinalizer(release, global.FinalizerName)
		logger.V(1).Info(">-> Update resource (Add finalizer)")
		if err := r.Update(ctx, release); err != nil {
			return ctrl.Result{}, fmt.Errorf("could not add the finalizer to the Release: %s", apiError(err))
		}
		return ctrl.Result{}, nil // we reschedule, to avoid an 'object has been modified' on next status update
	}

	GroomRelease(release, logger, r.ConfigStore)

	// ----------------------------------------------------------Setup our companion OCIRepository and wait its readiness
	ociRepository, waitMessage, reconcileError := r.handleOciRepository(op, global.PackageContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		// No need to requeue, as we should be notified when the OCI repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOci, waitMessage, false)
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content, if not in cache
	ociArtifact := ociRepository.Status.Artifact
	revision := ociArtifact.Revision
	// revision.txt names the revision in the folder, which lives as long as the pod
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
		err = r.Fetcher.Fetch(ctx, ociArtifact, helmRepositoryFolder)
		if err != nil {
			if err := misc.SafeEnsureEmpty(helmRepositoryFolder); err != nil {
				op.logger.Error(err, "Failed to empty helm repository folder", "folder", helmRepositoryFolder)
			}
			return r.reportError(op, NewReconcileError(fmt.Errorf("unable to fetch artifact: %w", err), errors.Is(err, errPackageLimit),
				"OCIRepository"), false)
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
	helmRepository, waitMessage, reconcileError := r.handleHelmRepository(op, revision, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		// No need to requeue, as we should be notified when the Helm repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmRepo, waitMessage, false)
	}

	// ---------------------------------------------------------- Retrieve package from cache, or load it
	// The loaded package holds defaults of the Configs, so its key holds the
	// version of the configuration.
	configVersion := r.ConfigStore.Version()
	packageKey := fmt.Sprintf("%s#%d", revision, configVersion)
	pckObj := r.PackageCache.Get(packageKey)
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
		originalSize, err := r.Fetcher.LoadFile(helmRepositoryFolder, "original.yaml", pck)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing package original.yaml file: %w", err), true, "OCIImage"), false)
		}
		// -------- And fetch status
		status := &kubopackage.Status{}
		statusSize, err := r.Fetcher.LoadFile(helmRepositoryFolder, "status.yaml", status)
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while parsing status.yaml file: %w", err), true, "OCIImage"), false)
		}
		op.pckContainer = &kubopackage.PckContainer{}
		err = op.pckContainer.SetPackage(pck, status, revision, r.ConfigStore, tmpl.PackageFunctions(r.AllowDNSLookups))
		if err != nil {
			return r.reportError(op, NewReconcileError(fmt.Errorf("error while loading package from image: %w", err), true, "OCIImage"), false)
		}
		// A package loaded while the Configs changed can hold two configurations
		if r.ConfigStore.Version() == configVersion {
			r.PackageCache.Set(packageKey, op.pckContainer, originalSize+statusSize)
		}
	}

	forceUpdate := false
	// Store protected in status
	protected := op.pckContainer.Package.Protected
	if op.release.Spec.Protected != nil {
		protected = *op.release.Spec.Protected
	}
	// The deletion webhook reads an empty printProtected as a protection not computed yet
	printProtected := misc.Ternary(protected, "X", "-")
	if protected != op.release.Status.Protected || printProtected != op.release.Status.PrintProtected {
		op.release.Status.Protected = protected
		op.release.Status.PrintProtected = printProtected
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
	if len(op.pckContainer.ContextConnectionDecls) > 0 {
		// The in-place substitution of resolved connections needs an owned
		// copy (the merged context can share subtrees with cached defaults)
		theContext = DeepCopyTree(theContext)
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
	// -------------------------------------------------------------------- Build model without input
	model, err := BuildModel(theContext, parameters, release, r.ConfigStore)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, true, "Model"), forceUpdate)
	}

	// -------------------------------------------------------------------- Render inputs
	inputs, err := op.pckContainer.Package.RenderInputs(model, release.Namespace)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, true, "Inputs"), forceUpdate)
	}
	// ------------------------------------------- Generate inputs from connection parameters
	generatedInputs, refBindings, err := GenerateRefInputs(op.pckContainer.ParamConnectionDecls, op.pckContainer.ContextConnectionDecls, parameters, theContext, release.Namespace, inputs)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, true, "ConnectionParameters"), forceUpdate)
	}
	inputs = append(inputs, generatedInputs...)
	// -------------------------------------------------------------------- Enrich model with inputs
	buildInputModelResult, err := BuildInputModel(op.ctx, r, inputs)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, false, "Inputs"), forceUpdate)
	}
	if !reflect.DeepEqual(buildInputModelResult.WatchedInputConnections, release.Status.WatchedInputConnections) {
		release.Status.WatchedInputConnections = buildInputModelResult.WatchedInputConnections
		forceUpdate = true
	}
	if !reflect.DeepEqual(buildInputModelResult.EffectiveInputConnections, release.Status.EffectiveInputConnections) {
		release.Status.EffectiveInputConnections = buildInputModelResult.EffectiveInputConnections
		forceUpdate = true
	}
	if len(buildInputModelResult.Messages) > 0 {
		for _, message := range buildInputModelResult.Messages {
			r.Event(op.release, "Warning", "MissingConnections", message)
		}
		r, err := r.updateStatus(op, kv1alpha1.ReleasePhaseWaitInputConnections, strings.Join(buildInputModelResult.Messages, " - "), forceUpdate)
		if err != nil {
			return ctrl.Result{}, err
		}
		if r.RequeueAfter > 0 {
			// It is a Requeue due to update status error
			return r, nil
		}
		return ctrl.Result{
			RequeueAfter: time.Second * 5,
		}, nil
	}
	// ------------------------------ Substitute resolved connections in place
	// (.Parameters.x / .Context.y) and keep .Inputs for the stanza only
	err = ApplyRefBindings(refBindings, buildInputModelResult.InputModel, parameters, theContext)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, false, "ConnectionParameters"), forceUpdate)
	}
	model["Inputs"] = buildInputModelResult.InputModel
	model["InputLists"] = buildInputModelResult.InputListModel
	// -------------------------------------------------------------------- Render all values
	rendered, err := op.pckContainer.Package.Render(model, release)
	if err != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("error on rendering: %w", err), errors.Is(err, tmpl.ErrLimit), "Rendering"),
			forceUpdate)
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
	if missing != "" {
		message := fmt.Sprintf("Waiting for the role '%s' to be ready", missing)
		r.Event(op.release, "Normal", "MissingDependency", message)
		r, err := r.updateStatus(op, kv1alpha1.ReleasePhaseWaitDependencies, message, forceUpdate)
		if err != nil {
			return ctrl.Result{}, err
		}
		if r.RequeueAfter > 0 {
			// It is a Requeue due to update status error
			return r, nil
		}
		return ctrl.Result{
			RequeueAfter: time.Second * 5,
		}, nil
	}

	// ---------------------------------------------------- Check our conditions in green state
	for idx, condition := range rendered.Conditions {
		ok, message, reconcileError := CheckCondition(op.ctx, r, condition)
		if reconcileError != nil {
			return r.reportError(op, atPlace(fmt.Sprintf("condition[%d]", idx), reconcileError), forceUpdate)
		}
		if !ok {
			message := fmt.Sprintf("Waiting for cond.#%d: %s", idx+1, message)
			r.Event(op.release, "Normal", "UnsetCondition", message)
			r, err := r.updateStatus(op, kv1alpha1.ReleasePhaseWaitConditions, message, forceUpdate)
			if err != nil {
				return ctrl.Result{}, err
			}
			if r.RequeueAfter > 0 {
				// It is a Requeue due to update status error
				return r, nil
			}
			return ctrl.Result{
				RequeueAfter: time.Second * 5,
			}, nil
		}
	}

	// -------------------------------------------------------- Now, we are ready to spawn the helmRelease(s)
	if !op.release.Spec.Suspended {
		op.helmReleaseStates = make(map[string]kv1alpha1.HelmReleaseState)
		for _, module := range op.pckContainer.Package.Modules {
			helmReleaseName := BuildHelmReleaseName(op.release.Name, module.Name)
			_, reconcileError := r.handleHelmRelease(op, rendered, helmReleaseName, module)
			//fmt.Printf("********** helmReleaseName: %s: %v\n", helmReleaseName, op.helmReleaseStates[helmReleaseName])
			if reconcileError != nil {
				return r.reportError(op, reconcileError, forceUpdate)
			}
		}
	}
	// -------------------------------------------------------- Adjust helmReleases status
	// And store helmReleases status
	readyReleases, allReady := computeReadyHelmReleases(op)
	if readyReleases != op.release.Status.ReadyReleases {
		op.release.Status.ReadyReleases = readyReleases
		forceUpdate = true
	}

	// Events generation and update setting are performed only if not already done
	if !reflect.DeepEqual(op.helmReleaseStates, op.release.Status.HelmReleaseStates) {
		op.release.Status.HelmReleaseStates = op.helmReleaseStates
		forceUpdate = true
		for k, v := range op.helmReleaseStates {
			if v.Status != "" {
				r.Event(op.release, misc.Ternary(v.Ready == "True", "Normal", "Warning"), fmt.Sprintf("HelmRelease:%s", k), v.Status)
			}
		}
	}

	var message string

	// Another loop to set the user error message in every reconciliation (idempotency)
	for k, v := range op.helmReleaseStates {
		if v.Ready != metav1.ConditionTrue && message == "" {
			status := v.Status
			if status == "" {
				status = "Deploying..."
			}
			if k == "noname" {
				message = fmt.Sprintf("HelmRelease: %s", status)
			} else {
				message = fmt.Sprintf("HelmRelease %s: %s", k, status)
			}
		}
	}

	if op.release.Spec.Suspended {
		return r.updateStatus(op, kv1alpha1.ReleasePhaseSuspended, message, forceUpdate)
	}
	if !allReady {
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmReleases, message, forceUpdate)
	}

	// --------------------------------------------------------------------------- We can now manage output connection
	/*
		NB: Output connection are updated only when helmRelease are OK. This will ensure
		- Output connection will be created only when the provider is ready.
		- If, later, one or several Helm releases are in error (failing update, ...), then existing connection are left untouched.
		  This is coherent with the fact the helmRelease update preserve the running, older, pods, so the service is still alive.
		  So the consumer services should not be notified in this case. So, don't touch connections.
	*/
	op.outputConnectionByName = make(map[string]kv1alpha1.ReleaseOutputConnection)
	op.outputConnectionK8sName = make(map[string]struct{})
	op.outputClusterConnectionK8sName = make(map[string]struct{})
	for idx, outputRendered := range rendered.Outputs {
		var reconcileError ReconcileError
		if outputRendered.Kind == kv1alpha1.KindClusterConnection {
			clusterConnectionName := BuildClusterConnectionName(op.release.Name, op.release.Namespace, outputRendered.Name)
			_, reconcileError = r.handleOutputClusterConnection(op, clusterConnectionName, outputRendered)
		} else {
			connectionName := BuildConnectionName(op.release.Name, outputRendered.Name)
			_, reconcileError = r.handleOutputConnection(op, connectionName, outputRendered)
		}
		if reconcileError != nil {
			return r.reportError(op, atPlace(fmt.Sprintf("output[%d]", idx), reconcileError), forceUpdate)
		}
	}

	// ----------------------------------------------------------- adjust Connection status
	// And store outputConnection status
	readyOutputConnections, allOutputConnectionReady := computeReadyConnection(op)
	if readyOutputConnections != op.release.Status.ReadyOutputConnections {
		op.release.Status.ReadyOutputConnections = readyOutputConnections
		forceUpdate = true
	}

	// Events generation and update setting are performed only if not already done
	if !reflect.DeepEqual(op.outputConnectionByName, op.release.Status.OutputConnectionByName) {
		op.release.Status.OutputConnectionByName = op.outputConnectionByName
		forceUpdate = true
		for k, v := range op.outputConnectionByName {
			if v.Phase != "" {
				eventMessage := fmt.Sprintf("Connection '%s' ready", k)
				if v.Phase != kv1alpha1.ConnectionPhaseReady {
					eventMessage = v.Message
				}
				r.Event(op.release, misc.Ternary(v.Phase == kv1alpha1.ConnectionPhaseReady, "Normal", "Warning"), fmt.Sprintf("connection:%s", k), eventMessage)
			}
		}
	}

	// Another loop to set the user error message in every reconciliation (idempotency). A connection in a phase other
	// than READY comes before a connection that no controller checked yet.
	outputs := slices.Sorted(maps.Keys(op.outputConnectionByName))
	for _, k := range outputs {
		if v := op.outputConnectionByName[k]; v.Phase != "" && v.Phase != kv1alpha1.ConnectionPhaseReady && message == "" {
			message = fmt.Sprintf("Connection '%s': %s", k, v.Message)
		}
	}
	for _, k := range outputs {
		if op.outputConnectionByName[k].Phase == "" && message == "" {
			message = fmt.Sprintf("Connection '%s': %s", k, r.uncheckedConnection())
		}
	}

	// phase is kv1alpha1.ReleasePhaseReady
	if !allOutputConnectionReady {
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOutputConnections, message, forceUpdate)
	}
	// ---------------------------------------------------------- Find orphan connection, and delete them
	//
	outputConnections, listErr := r.FindOutputConnectionsFromRelease(ctx, types.NamespacedName{Namespace: release.GetNamespace(), Name: release.Name})
	if listErr != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("could not list its Connections: %s", apiError(listErr.GetBaseError())), false,
			"ConnectionAccess"), forceUpdate)
	}
	for _, connection := range outputConnections {
		_, ok := op.outputConnectionK8sName[connection.Name]
		if !ok {
			// Connection with ownerReference on ourselves, but not managed. Delete it
			logger.V(0).Info("Deleting orphan connection", "name", connection.Name)
			err := r.Delete(ctx, &connection)
			if err != nil {
				logger.Error(errors.New(apiError(err)), "unable to delete connection", "connection", connection.Name)
			}
		}
	}
	// ---------------------------------------------------------- Find orphan clusterConnection, and delete them
	//
	outputClusterConnections, listErr := r.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: release.GetNamespace(), Name: release.Name})
	if listErr != nil {
		return r.reportError(op, NewReconcileError(fmt.Errorf("could not list its ClusterConnections: %s", apiError(listErr.GetBaseError())),
			false, "ClusterConnectionAccess"), forceUpdate)
	}
	for _, clusterConnection := range outputClusterConnections {
		_, ok := op.outputClusterConnectionK8sName[clusterConnection.Name]
		if !ok {
			// Connection with ownerReference on ourselves, but not managed. Delete it
			logger.V(0).Info("Deleting orphan clusterConnection", "name", clusterConnection.Name)
			err := r.Delete(ctx, &clusterConnection)
			if err != nil {
				logger.Error(errors.New(apiError(err)), "unable to delete clusterConnection", "clusterConnection", clusterConnection.Name)
			}
		}
	}
	// Final return
	return r.updateStatus(op, kv1alpha1.ReleasePhaseReady, "", forceUpdate)
}

// deleteOutputClusterConnections deletes the ClusterConnections that the
// Release created, and returns the first error once each deletion is tried.
func (r *ReleaseReconciler) deleteOutputClusterConnections(op *releaseOperation) error {
	clusterConnections, listErr := r.FindOutputClusterConnectionsFromRelease(op.ctx, op.request.NamespacedName)
	if listErr != nil {
		return fmt.Errorf("could not list the ClusterConnections of the Release: %s", apiError(listErr.GetBaseError()))
	}
	var failed error
	for i := range clusterConnections {
		if err := client.IgnoreNotFound(r.Delete(op.ctx, &clusterConnections[i])); err != nil {
			op.logger.Error(errors.New(apiError(err)), "Failed to delete ClusterConnection", "clusterConnection", clusterConnections[i].Name)
			if failed == nil {
				failed = fmt.Errorf("could not delete the ClusterConnections of the Release: %s", apiError(err))
			}
		}
	}
	return failed
}

func computeReadyConnection(op *releaseOperation) (string, bool) {
	cnt := 0
	for _, outputConnectionState := range op.outputConnectionByName {
		if outputConnectionState.Phase == kv1alpha1.ConnectionPhaseReady {
			cnt++
		}
	}
	return fmt.Sprintf("%d/%d", cnt, len(op.outputConnectionByName)), cnt == len(op.outputConnectionByName)
}

// If error is 'fatal', this means it is due to something which can't be fixed with retry (i.e: invalid image).
// In such case, set status.phase = ERROR, log and don't retry
func (r *ReleaseReconciler) reportError(op *releaseOperation, rErr ReconcileError, forceUpdate bool) (ctrl.Result, error) {
	ctrlResult, err2 := r.updateStatus(op, kv1alpha1.ReleasePhaseError, rErr.Error(), forceUpdate)
	if err2 != nil {
		return ctrl.Result{}, rErr // Will retry
	}
	if rErr.GetEventReason() != "" && rErr.GetBaseError() != nil {
		r.Event(op.release, "Warning", rErr.GetEventReason(), rErr.Error())
	}
	if rErr.IsFatal() {
		op.logger.Error(rErr, "Wait for this to be fixed")
		return ctrlResult, nil
	}
	return ctrl.Result{}, rErr
}

func (r *ReleaseReconciler) updateStatus(op *releaseOperation, phase kv1alpha1.ReleasePhase, message string, force bool) (ctrl.Result, error) {
	if phase == kv1alpha1.ReleasePhaseReady {
		r.RoleStore.RegisterRelease(op.request.NamespacedName, op.roles)
	} else {
		r.RoleStore.UnRegisterRelease(op.request.NamespacedName)
	}
	if op.release.Status.Phase == phase && op.release.Status.Message == message && !force {
		op.logger.V(1).Info("Release phase and message are already up-to-date", "phase", phase, "message", message)
		return ctrl.Result{}, nil
	}
	op.logger.V(1).Info("Updating status", "newPhase", phase, "oldPhase", op.release.Status.Phase, "newMessage", message, "force", force)
	op.release.Status.Phase = phase
	op.release.Status.Message = message
	err := r.Status().Update(op.ctx, op.release)
	if err != nil {
		//fmt.Printf("***********************: %s    (%T)\n", phase, err)
		if r.statusFailures.fail(op.request.NamespacedName) {
			return ctrl.Result{}, fmt.Errorf("could not update the Release status: %s", apiError(err))
		}
		op.logger.V(1).Info("Error updating status. Hidden as first one", "phase", phase)
		return ctrl.Result{RequeueAfter: time.Millisecond * 200}, nil
	}
	//fmt.Printf("-----------------------: %s\n", phase)
	r.statusFailures.clear(op.request.NamespacedName)
	return ctrl.Result{}, err
}

const ReleaseIndexOnOutputConnection = "releaseIndexOnOutputConnection"

// OutputConnectionReleaseKeys returns the keys of a Connection in the index
// ReleaseIndexOnOutputConnection: the name of the Release that controls it.
func OutputConnectionReleaseKeys(connection *kv1alpha1.Connection) []string {
	owner := metav1.GetControllerOf(connection)
	if owner == nil {
		return []string{}
	}
	return []string{owner.Name}
}

func (r *ReleaseReconciler) FindOutputConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError) {
	connections := kv1alpha1.ConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ReleaseIndexOnOutputConnection, release.Name),
		Namespace:     release.Namespace,
	}
	err := r.List(ctx, &connections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindOutputConnectionFromRelease(): Unable to find release bindings: %w", err), false, "")
		}
		return []kv1alpha1.Connection{}, nil
	}
	return connections.Items, nil
}

const ReleaseIndexOnOutputClusterConnection = "ReleaseIndexOnOutputClusterConnection"

// OutputClusterConnectionReleaseKeys returns the keys of a ClusterConnection in
// the index ReleaseIndexOnOutputClusterConnection: the namespace and the name
// of its parent Release.
func OutputClusterConnectionReleaseKeys(clusterConnection *kv1alpha1.ClusterConnection) []string {
	parent := clusterConnection.Spec.ParentRelease
	if parent == nil {
		return []string{}
	}
	return []string{fmt.Sprintf("%s:%s", parent.Namespace, parent.Name)}
}

func (r *ReleaseReconciler) FindOutputClusterConnectionsFromRelease(ctx context.Context, release types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	clusterConnections := kv1alpha1.ClusterConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ReleaseIndexOnOutputClusterConnection, fmt.Sprintf("%s:%s", release.Namespace, release.Name)),
	}
	err := r.List(ctx, &clusterConnections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindOutputClusterConnectionsFromRelease(): Unable to find release bindings: %w", err), false, "")
		}
		return []kv1alpha1.ClusterConnection{}, nil
	}
	return clusterConnections.Items, nil
}

const ContractIndexOnConnection = "contractIndexOnConnection"

func (r *ReleaseReconciler) FindConnectionsFromContract(ctx context.Context, namespace string, contract string) ([]kv1alpha1.Connection, ReconcileError) {
	connections := &kv1alpha1.ConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ContractIndexOnConnection, contract),
		Namespace:     namespace,
	}
	err := r.List(ctx, connections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindConnectionsFromContract(): Unable to find contract bindings: %w", err), false, "")
		}
	}
	return connections.Items, nil
}

const ContractIndexOnClusterConnection = "contractIndexOnClusterConnection"

func (r *ReleaseReconciler) FindClusterConnectionsFromContract(ctx context.Context, contract string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	clusterConnections := &kv1alpha1.ClusterConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(ContractIndexOnClusterConnection, contract),
	}
	err := r.List(ctx, clusterConnections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindClusterConnectionsFromContract(): Unable to find contract bindings: %w", err), false, "")
		}
	}
	return clusterConnections.Items, nil
}
