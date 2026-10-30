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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/misc"

	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// waitOciRepository is the message of a Release that waits for its OCIRepository.
const waitOciRepository = "Wait OCI repository"

// releaseInsecureSourceError identifies a Release source rejected by transport policy.
type releaseInsecureSourceError struct{}

// Error returns the fixed API-field diagnostic without package data.
func (*releaseInsecureSourceError) Error() string {
	return "Release.spec.package.insecure is not allowed without a matching packageRedirect"
}

// errReleaseInsecureSource is the sentinel for Release-controlled HTTP transport without a redirect.
var errReleaseInsecureSource = &releaseInsecureSourceError{}

// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=ocirepositories,verbs=get;list;watch;create;patch

// handleOciRepository returns the OCIRepository of the Release once ready, or
// otherwise the message of the wait.
func (r *ReleaseReconciler) handleOciRepository(op *releaseOperation) (*sourcev1.OCIRepository, string, ReconcileError) {
	// Fetch associated OCIRepository
	ociRepository := &sourcev1.OCIRepository{}
	err := r.Get(op.ctx, types.NamespacedName{Name: op.ociRepositoryName, Namespace: op.release.Namespace}, ociRepository)
	if err != nil {
		//logger.V(1).Info("Unable to fetch OCI Repository", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, "", NewReconcileError(fmt.Errorf("on '%s': %w", op.ociRepositoryName, err), false, "OCIRepositoryAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create associated OCIRepository", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		err := r.createOciRepository(op, global.PackageContentMediaType, sourcev1.OCILayerExtract)
		if err != nil {
			if errors.Is(err, errReleaseInsecureSource) {
				return nil, "", NewReconcileError(errReleaseInsecureSource, true, "ReleaseInsecureSource")
			}
			return nil, "", NewReconcileError(err, false, "OCIRepositoryCreateFailed")
		}
		r.Event(op.release, "Normal", "OCIRepositoryCreated", fmt.Sprintf("Created OCIRepository %q", op.ociRepositoryName))
		// Caller will Requeue, waiting for OCI
		return nil, waitOciRepository, nil
	} else {
		if !metav1.IsControlledBy(ociRepository, op.release) {
			return nil, "", NewReconcileError(fmt.Errorf("OCIRepository '%s' exists and is not controlled by this Release", op.ociRepositoryName),
				false, "OCIRepositoryOwner")
		}
		changed, err := r.patchOciRepository(op, ociRepository, global.PackageContentMediaType, sourcev1.OCILayerExtract)
		if err != nil {
			if errors.Is(err, errReleaseInsecureSource) {
				return nil, "", NewReconcileError(errReleaseInsecureSource, true, "ReleaseInsecureSource")
			}
			return nil, "", NewReconcileError(err, false, "OCIRepositoryPatchFailed")
		}
		if changed {
			op.logger.V(0).Info("OCI repository updated", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		} else {
			op.logger.V(1).Info("OCI repository unchanged", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		}
	}
	statusByType := buildConditionStatusByType(ociRepository.Status.Conditions, "OCIRepository", op.ociRepositoryName, op.logger)
	if staleReady(ociRepository.Status.Conditions, ociRepository.Status.ObservedGeneration, ociRepository.Generation) {
		delete(statusByType, "Ready")
	}

	if statusByType["Ready"] != metav1.ConditionTrue {
		readyCondition, ok := statusByType["Ready"]
		if !ok || readyCondition == metav1.ConditionUnknown {
			//  Caller will requeue, waiting for OCI
			return nil, waitOciRepository, nil
		}
		if readyCondition == metav1.ConditionFalse {
			// Flux retries on its own, and its change of status reconciles the Release
			return nil, notReadySource("OCIRepository", op.ociRepositoryName, ociRepository.Status.Conditions), nil
		}
		// Something wrong with OCI repo
		return nil, "", NewReconcileError(fmt.Errorf("invalid status '%s' for Ready condition on OCIRepository '%s'", statusByType["Ready"], op.ociRepositoryName), true, "OCIRepositoryNotReady")
	}
	if ociRepository.Status.Artifact == nil {
		//return nil, NewReconcileError(fmt.Errorf("null status.artifact on OCIRepository '%s'", name), false, "OCIRepositoryNotReady")
		//  Caller will requeue, waiting for OCI
		return nil, waitOciRepository, nil
	}
	return ociRepository, "", nil
}

// notReadySource is the message of a Release whose Flux source, of the kind
// and the name given, is not ready, with the message of its Ready condition.
func notReadySource(kind string, name string, conditions []metav1.Condition) string {
	message := fmt.Sprintf("%s '%s' is not ready", kind, name)
	if ready := apimeta.FindStatusCondition(conditions, "Ready"); ready != nil && ready.Message != "" {
		message += ": " + ready.Message
	}
	return message
}

// OCIRepositoryOptions controls how package sources populate OCIRepository specifications.
// Its zero value preserves Release verification through redirects and refuses direct insecure Release sources.
type OCIRepositoryOptions struct {
	// AllowRedirectVerifyDrop lets a matching redirect without verify remove Release verification.
	AllowRedirectVerifyDrop bool
	// AllowReleaseInsecureSource permits Release-controlled HTTP transport without a matching redirect.
	AllowReleaseInsecureSource bool
}

// PopulateOciRepository fills the source specification using a matching redirect or the Release package.
func PopulateOciRepository(
	ociRepository *sourcev1.OCIRepository,
	release *kv1alpha1.Release,
	mediaType, ociOperation string,
	configStore configstore.ConfigStore,
	options OCIRepositoryOptions,
) error {
	packageRedirectSpec, newUrl := configStore.GetPackageRedirect(fmt.Sprintf("%s:%s", release.Spec.Package.Repository, release.Spec.Package.Tag))
	if packageRedirectSpec != nil {
		repo, tag, err := misc.DecodeImageUrl(newUrl)
		if err != nil {
			return fmt.Errorf("invalid OCI repository URL: %w", err)
		}
		ociRepository.Spec.URL = fmt.Sprintf("oci://%s", repo)
		ociRepository.Spec.Reference = &sourcev1.OCIRepositoryRef{
			Tag: tag,
		}
		ociRepository.Spec.Provider = packageRedirectSpec.Provider
		ociRepository.Spec.SecretRef = packageRedirectSpec.SecretRef
		ociRepository.Spec.Verify = packageRedirectSpec.Verify
		if ociRepository.Spec.Verify == nil && !options.AllowRedirectVerifyDrop {
			ociRepository.Spec.Verify = release.Spec.Package.Verify
		}
		ociRepository.Spec.ServiceAccountName = packageRedirectSpec.ServiceAccountName
		ociRepository.Spec.CertSecretRef = packageRedirectSpec.CertSecretRef
		ociRepository.Spec.ProxySecretRef = packageRedirectSpec.ProxySecretRef
		ociRepository.Spec.Interval = packageRedirectSpec.Interval
		ociRepository.Spec.Timeout = packageRedirectSpec.Timeout
		ociRepository.Spec.Ignore = packageRedirectSpec.Ignore
		ociRepository.Spec.Insecure = packageRedirectSpec.Insecure
		ociRepository.Spec.Suspend = packageRedirectSpec.Suspend
	} else {
		if release.Spec.Package.Insecure && !options.AllowReleaseInsecureSource {
			return errReleaseInsecureSource
		}
		ociRepository.Spec.URL = fmt.Sprintf("oci://%s", release.Spec.Package.Repository)
		ociRepository.Spec.Reference = &sourcev1.OCIRepositoryRef{
			Tag: release.Spec.Package.Tag,
		}
		ociRepository.Spec.Provider = release.Spec.Package.Provider
		ociRepository.Spec.SecretRef = release.Spec.Package.SecretRef
		ociRepository.Spec.Verify = release.Spec.Package.Verify
		ociRepository.Spec.ServiceAccountName = release.Spec.Package.ServiceAccountName
		ociRepository.Spec.CertSecretRef = release.Spec.Package.CertSecretRef
		ociRepository.Spec.ProxySecretRef = release.Spec.Package.ProxySecretRef
		ociRepository.Spec.Interval = release.Spec.Package.Interval
		ociRepository.Spec.Timeout = release.Spec.Package.Timeout
		ociRepository.Spec.Ignore = release.Spec.Package.Ignore
		ociRepository.Spec.Insecure = release.Spec.Package.Insecure
		ociRepository.Spec.Suspend = release.Spec.Package.Suspend
	}
	//ociRepository.Spec.LayerSelector = nil // Wll take the first one
	ociRepository.Spec.LayerSelector = &sourcev1.OCILayerSelector{
		MediaType: mediaType,
		Operation: ociOperation,
	}
	return nil
}

func (r *ReleaseReconciler) createOciRepository(op *releaseOperation, mediaType string, ociOperation string) error {
	ociRepository := &sourcev1.OCIRepository{}
	ociRepository.SetName(op.ociRepositoryName)
	ociRepository.SetNamespace(op.release.Namespace)
	err := PopulateOciRepository(ociRepository, op.release, mediaType, ociOperation, r.ConfigStore, r.OCIRepositoryOptions)
	if err != nil {
		return fmt.Errorf("error while creating associated OCIRepository '%s': %w", op.ociRepositoryName, err)
	}
	err = ctrl.SetControllerReference(op.release, ociRepository, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set owner reference on OCIRepository '%s': %w", op.ociRepositoryName, err)
	}
	if err = r.Create(op.ctx, ociRepository); err != nil {
		return fmt.Errorf("error while creating associated OCIRepository '%s': %w", op.ociRepositoryName, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchOciRepository(op *releaseOperation, ociRepository *sourcev1.OCIRepository, mediaType string, ociOperation string) (bool, error) {
	originalGeneration := ociRepository.Generation
	patch := mergeFromWithUID(ociRepository)
	err := PopulateOciRepository(ociRepository, op.release, mediaType, ociOperation, r.ConfigStore, r.OCIRepositoryOptions)
	if err != nil {
		if errors.Is(err, errReleaseInsecureSource) && ociRepository.Spec.Insecure && metav1.IsControlledBy(ociRepository, op.release) {
			base := ociRepository.DeepCopy()
			base.UID = ""
			secured := ociRepository.DeepCopy()
			secured.Spec.Insecure = false
			if patchErr := r.Patch(op.ctx, secured, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); patchErr != nil {
				return false, errors.New("could not disable insecure transport on associated OCIRepository")
			}
		}
		return false, fmt.Errorf("error while patching OCIRepository '%s': %w", ociRepository.Name, err)
	}
	err = r.Patch(op.ctx, ociRepository, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching OCIRepository '%s': %w", ociRepository.Name, err)
	}
	return originalGeneration != ociRepository.Generation, nil
}
