package controller

import (
	"fmt"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/misc"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleOciRepository(op *releaseOperation, mediaType string, ociOperation string) (*sourcev1b2.OCIRepository, ReconcileError) {
	// Fetch associated OCIRepository
	ociRepository := &sourcev1b2.OCIRepository{}
	err := r.Get(op.ctx, types.NamespacedName{Name: op.ociRepositoryName, Namespace: op.release.Namespace}, ociRepository)
	if err != nil {
		//logger.V(1).Info("Unable to fetch OCI Repository", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on '%s': %w", op.ociRepositoryName, err), false, "OCIRepositoryAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create associated OCIRepository", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		err := r.createOciRepository(op, mediaType, ociOperation)
		if err != nil {
			return nil, NewReconcileError(err, false, "OCIRepositoryCreateFailed")
		}
		r.Event(op.release, "Normal", "OCIRepositoryCreated", fmt.Sprintf("Created OCIRepository %q", op.ociRepositoryName))
		// Caller will Requeue, waiting for OCI
		return nil, nil
	} else {
		changed, err := r.patchOciRepository(op, ociRepository, mediaType, ociOperation)
		if err != nil {
			return nil, NewReconcileError(err, false, "OCIRepositoryPatchFailed")
		}
		if changed {
			op.logger.V(0).Info("OCI repository updated", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		} else {
			op.logger.V(1).Info("OCI repository unchanged", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		}
	}
	statusByType := buildConditionStatusByType(ociRepository.Status.Conditions, "OCIRepository", op.ociRepositoryName, op.logger)

	if statusByType["Ready"] != metav1.ConditionTrue {
		readyCondition, ok := statusByType["Ready"]
		if !ok || readyCondition == metav1.ConditionUnknown || readyCondition == metav1.ConditionFalse {
			//  Caller will requeue, waiting for OCI
			return nil, nil
		}
		// Something wrong with OCI repo
		return nil, NewReconcileError(fmt.Errorf("invalid status '%s' for Ready condition on OCIRepository '%s'", statusByType["Ready"], op.ociRepositoryName), true, "OCIRepositoryNotReady")
	}
	if ociRepository.Status.Artifact == nil {
		//return nil, NewReconcileError(fmt.Errorf("null status.artifact on OCIRepository '%s'", name), false, "OCIRepositoryNotReady")
		//  Caller will requeue, waiting for OCI
		return nil, nil
	}
	return ociRepository, nil
}

func PopulateOciRepository(ociRepository *sourcev1b2.OCIRepository, release *kv1alpha1.Release, mediaType string, ociOperation string, configStore configstore.ConfigStore) error {
	packageRedirectSpec, newUrl := configStore.GetPackageRedirect(fmt.Sprintf("%s:%s", release.Spec.Package.Repository, release.Spec.Package.Tag))
	if packageRedirectSpec != nil {
		repo, tag, err := misc.DecodeImageUrl(newUrl)
		if err != nil {
			return fmt.Errorf("invalid OCI repository URL: %w", err)
		}
		ociRepository.Spec.URL = fmt.Sprintf("oci://%s", repo)
		ociRepository.Spec.Reference = &sourcev1b2.OCIRepositoryRef{
			Tag: tag,
		}
		ociRepository.Spec.Provider = packageRedirectSpec.Provider
		ociRepository.Spec.SecretRef = packageRedirectSpec.SecretRef
		ociRepository.Spec.Verify = packageRedirectSpec.Verify
		ociRepository.Spec.ServiceAccountName = packageRedirectSpec.ServiceAccountName
		ociRepository.Spec.CertSecretRef = packageRedirectSpec.CertSecretRef
		ociRepository.Spec.ProxySecretRef = packageRedirectSpec.ProxySecretRef
		ociRepository.Spec.Interval = packageRedirectSpec.Interval
		ociRepository.Spec.Timeout = packageRedirectSpec.Timeout
		ociRepository.Spec.Ignore = packageRedirectSpec.Ignore
		ociRepository.Spec.Insecure = packageRedirectSpec.Insecure
		ociRepository.Spec.Suspend = packageRedirectSpec.Suspend
	} else {
		ociRepository.Spec.URL = fmt.Sprintf("oci://%s", release.Spec.Package.Repository)
		ociRepository.Spec.Reference = &sourcev1b2.OCIRepositoryRef{
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
	ociRepository.Spec.LayerSelector = &sourcev1b2.OCILayerSelector{
		MediaType: mediaType,
		Operation: ociOperation,
	}
	return nil
}

func (r *ReleaseReconciler) createOciRepository(op *releaseOperation, mediaType string, ociOperation string) error {
	ociRepository := &sourcev1b2.OCIRepository{}
	ociRepository.SetName(op.ociRepositoryName)
	ociRepository.SetNamespace(op.release.Namespace)
	err := PopulateOciRepository(ociRepository, op.release, mediaType, ociOperation, r.ConfigStore)
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

func (r *ReleaseReconciler) patchOciRepository(op *releaseOperation, ociRepository *sourcev1b2.OCIRepository, mediaType string, ociOperation string) (bool, error) {
	originalGeneration := ociRepository.Generation
	patch := client.MergeFrom(ociRepository.DeepCopy())
	err := PopulateOciRepository(ociRepository, op.release, mediaType, ociOperation, r.ConfigStore)
	if err != nil {
		return false, fmt.Errorf("error while patching OCIRepository '%s': %w", ociRepository.Name, err)
	}
	err = r.Patch(op.ctx, ociRepository, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching OCIRepository '%s': %w", ociRepository.Name, err)
	}
	return originalGeneration != ociRepository.Generation, nil
}
