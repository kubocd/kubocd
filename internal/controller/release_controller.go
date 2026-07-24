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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/cache"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/rolestore"
	"os"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/fluxcd/pkg/http/fetch"
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
	Logger           logr.Logger
	Fetcher          *fetch.ArchiveFetcher
	ServerRoot       string
	HelmRepoAdvAddr  string
	PackageCache     cache.Cache
	ConfigStore      configstore.ConfigStore
	RoleStore        rolestore.RoleStore
	statusErrorCount int
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
		// Remove outputClusterConnection
		clusterConnections, err := r.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: release.Namespace, Name: release.Name})
		if err != nil {
			// Just log, without any other action
			op.logger.Error(err, "Failed to list child output ClusterConnection")
		} else {
			for _, clusterConnection := range clusterConnections {
				err := r.Delete(ctx, &clusterConnection)
				if err != nil {
					// Just log, without any other action
					op.logger.Error(err, "Failed to delete ClusterConnection '%s'", clusterConnection.Name)
				}
			}
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
		err := r.Update(ctx, release)
		return ctrl.Result{}, err // we reschedule, to avoid an 'object has been modified' on next status update
		//if err != nil {
		//	return ctrl.Result{}, err
		//}
	}

	GroomRelease(release, logger, r.ConfigStore)

	// ----------------------------------------------------------Setup our companion OCIRepository and wait its readiness
	ociRepository, reconcileError := r.handleOciRepository(op, global.PackageContentMediaType, "extract")
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if ociRepository == nil {
		// set phase to WAIT_OCI
		// No need to requeue, as we should be notified when the OCI repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOci, "Wait OCI repository", false)
	}

	// ---------------------------------- At this point, we have an effective primary OCI repo, so we can fetch the content, if not in cache
	ociArtifact := ociRepository.Status.Artifact
	revision := ociArtifact.Revision
	revisionFile := path.Join(helmRepositoryFolder, "revision.txt")

	// The following flag mark the case we update the helmRepository contents. So, the index.yaml may be changed (In case
	// of helm chart change) and we need to delete/recreate the helmRepository resource to force index reload
	invalidate := false

	revisionCached, err := os.ReadFile(revisionFile)
	if err != nil {
		if !os.IsNotExist(err) {
			op.logger.Error(err, "Failed to read revision file")
		}
		// If notFound, it is a normal case. revision == "", so load it below
	}
	if string(revisionCached) != revision {
		invalidate = true // If in initial load, invalidate==true will have no effect, as helmRepository resource does not exist yet
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
	helmRepository, reconcileError := r.handleHelmRepository(op, invalidate, repoUrl)
	if reconcileError != nil {
		return r.reportError(op, reconcileError, false)
	}
	if helmRepository == nil {
		// set phase to WAIT_HELM_REPO
		// No need to requeue, as we should be notified when the Helm repo status will change
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitHelmRepo, "Wait Helm Repository", false)
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
		err := op.pckContainer.SetPackage(pck, status, revision, r.ConfigStore)
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
	// -------------------------------------------------------------------- Build model without input
	model := BuildModel(theContext, parameters, release, r.ConfigStore)

	// -------------------------------------------------------------------- Render inputs
	inputs, err := op.pckContainer.Package.RenderInputs(model, release.Namespace)
	if err != nil {
		return r.reportError(op, NewReconcileError(err, true, "Inputs"), forceUpdate)
	}
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
	model["Inputs"] = buildInputModelResult.InputModel
	model["InputLists"] = buildInputModelResult.InputListModel
	// -------------------------------------------------------------------- Render all values
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
			message = fmt.Sprintf("HelmRelease %s: %s", k, v.Status)
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
	for _, outputRendered := range rendered.Outputs {
		var reconcileError ReconcileError
		if outputRendered.Kind == kv1alpha1.KindClusterConnection {
			clusterConnectionName := BuildClusterConnectionName(op.release.Name, op.release.Namespace, outputRendered.Name)
			_, reconcileError = r.handleOutputClusterConnection(op, clusterConnectionName, outputRendered)
		} else {
			connectionName := BuildConnectionName(op.release.Name, outputRendered.Name)
			_, reconcileError = r.handleOutputConnection(op, connectionName, outputRendered)
		}
		if reconcileError != nil {
			return r.reportError(op, reconcileError, forceUpdate)
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

	// Another loop to set the user error message in every reconciliation (idempotency)
	for k, v := range op.outputConnectionByName {
		if v.Phase != "" && v.Phase != kv1alpha1.ConnectionPhaseReady && message == "" {
			message = fmt.Sprintf("Connection '%s': %s", k, v.Message)
		}
	}

	// phase is kv1alpha1.ReleasePhaseReady
	if !allOutputConnectionReady {
		return r.updateStatus(op, kv1alpha1.ReleasePhaseWaitOutputConnections, message, forceUpdate)
	}
	// ---------------------------------------------------------- Find orphan connection, and delete them
	//
	outputConnections, err := r.FindOutputConnectionsFromRelease(ctx, types.NamespacedName{Namespace: release.GetNamespace(), Name: release.Name})
	for _, connection := range outputConnections {
		_, ok := op.outputConnectionK8sName[connection.Name]
		if !ok {
			// Connection with ownerReference on ourselves, but not managed. Delete it
			logger.V(0).Info("Deleting orphan connection", "name", connection.Name)
			err := r.Delete(ctx, &connection)
			if err != nil {
				logger.Error(err, "unable to delete connection", "connection", connection.Name)
			}
		}
	}
	// ---------------------------------------------------------- Find orphan clusterConnection, and delete them
	//
	outputClusterConnections, err := r.FindOutputClusterConnectionsFromRelease(ctx, types.NamespacedName{Namespace: release.GetNamespace(), Name: release.Name})
	for _, clusterConnection := range outputClusterConnections {
		_, ok := op.outputClusterConnectionK8sName[clusterConnection.Name]
		if !ok {
			// Connection with ownerReference on ourselves, but not managed. Delete it
			logger.V(0).Info("Deleting orphan clusterConnection", "name", clusterConnection.Name)
			err := r.Delete(ctx, &clusterConnection)
			if err != nil {
				logger.Error(err, "unable to delete clusterConnection", "clusterConnection", clusterConnection.Name)
			}
		}
	}
	// Final return
	return r.updateStatus(op, kv1alpha1.ReleasePhaseReady, "", forceUpdate)
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
	op.logger.V(1).Info("Updating status", "newPhase", phase, "oldPhase", op.release.Status.Phase, "newMessage", message, "oldMessage", op.release.Status.Message, "force", force)
	op.release.Status.Phase = phase
	op.release.Status.Message = message
	err := r.Status().Update(op.ctx, op.release)
	if err != nil {
		//fmt.Printf("***********************: %s    (%T)\n", phase, err)
		if r.statusErrorCount > 0 {
			return ctrl.Result{}, err
		}
		r.statusErrorCount++
		op.logger.V(1).Info("Error updating status. Hidden as first one", "phase", phase)
		return ctrl.Result{RequeueAfter: time.Millisecond * 200}, nil
	}
	//fmt.Printf("-----------------------: %s\n", phase)
	r.statusErrorCount = 0
	return ctrl.Result{}, err
}

const ReleaseIndexOnOutputConnection = "releaseIndexOnOutputConnection"

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

const InterfaceIndexOnConnection = "interfaceIndexOnConnection"

func (r *ReleaseReconciler) FindConnectionsFromInterface(ctx context.Context, namespace string, iface string) ([]kv1alpha1.Connection, ReconcileError) {
	connections := &kv1alpha1.ConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(InterfaceIndexOnConnection, iface),
		Namespace:     namespace,
	}
	err := r.List(ctx, connections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindConnectionsFromInterface(): Unable to find interface bindings: %w", err), false, "")
		}
	}
	return connections.Items, nil
}

const InterfaceIndexOnClusterConnection = "interfaceIndexOnClusterConnection"

func (r *ReleaseReconciler) FindClusterConnectionsFromInterface(ctx context.Context, iface string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	clusterConnections := &kv1alpha1.ClusterConnectionList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(InterfaceIndexOnClusterConnection, iface),
	}
	err := r.List(ctx, clusterConnections, listOps)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("FindClusterConnectionsFromInterface(): Unable to find interface bindings: %w", err), false, "")
		}
	}
	return clusterConnections.Items, nil
}
