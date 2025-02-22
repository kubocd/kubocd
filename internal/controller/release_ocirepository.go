package controller

import (
	"fmt"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleOciRepository(op *operation) (*sourcev1b2.OCIRepository, *ReconcileError) {
	// Fetch associated OCIRepository
	ociRepository := &sourcev1b2.OCIRepository{}
	err := r.Get(op.ctx, types.NamespacedName{Name: op.release.Name, Namespace: op.release.Namespace}, ociRepository)
	if err != nil {
		//logger.V(1).Info("Unable to fetch OCI Repository", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(err, false, "OCIRepositoryAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create associated OCI repository")
		err := r.createOciRepository(op)
		if err != nil {
			return nil, NewReconcileError(err, false, "OCIRepositoryCreateFailed")
		}
		r.Event(op.release, "Normal", "OCIRepositoryCreated", fmt.Sprintf("Created OCI repository %q", op.release.Name))
		// Caller will Requeue, waiting for OCI
		return nil, nil
	} else {
		changed, err := r.patchOciRepository(op, ociRepository)
		if err != nil {
			return nil, NewReconcileError(err, false, "OCIRepositoryPatchFailed")
		}
		if changed {
			op.logger.V(0).Info("OCI repository updated")
		} else {
			op.logger.V(1).Info("OCI repository unchanged")
		}
	}
	statusByType := buildConditionStatusByType(ociRepository.Status.Conditions, op.logger)

	if statusByType["Ready"] != metav1.ConditionTrue {
		readyCondition, ok := statusByType["Ready"]
		if !ok || readyCondition == metav1.ConditionUnknown {
			//  Caller will requeue, waiting for OCI
			return nil, nil
		}
		// Something wrong with OCI repo
		return nil, NewReconcileError(fmt.Errorf("invalid status '%s' for Ready condition", statusByType["Ready"]), true, "OCIRepositoryNotReady")
	}
	if ociRepository.Status.Artifact == nil {
		//return nil, NewReconcileError(fmt.Errorf("null status.artifact"), false, "OCIRepositoryNotReady")
		//  Caller will requeue, waiting for OCI
		return nil, nil
	}
	return ociRepository, nil
}

func populateOciRepository(ociRepository *sourcev1b2.OCIRepository, release *kubocdv1alpha1.Release) {
	ociRepository.Spec.URL = fmt.Sprintf("oci://%s", release.Spec.Service.Repository)
	ociRepository.Spec.Reference = &sourcev1b2.OCIRepositoryRef{
		Tag: release.Spec.Service.Tag,
	}
	ociRepository.Spec.LayerSelector = nil // Wll take the first one
	ociRepository.Spec.LayerSelector = &sourcev1b2.OCILayerSelector{
		MediaType: global.ServiceManifestMediaType,
		//MediaType: "application/vnd.kubotal.kubocd.service.module.podinfo.content.v1.tar+gzip",
		Operation: "extract",
	}
	ociRepository.Spec.Provider = release.Spec.Service.Provider
	ociRepository.Spec.SecretRef = release.Spec.Service.SecretRef
	ociRepository.Spec.Verify = release.Spec.Service.Verify
	ociRepository.Spec.ServiceAccountName = release.Spec.Service.ServiceAccountName
	ociRepository.Spec.CertSecretRef = release.Spec.Service.CertSecretRef
	ociRepository.Spec.ProxySecretRef = release.Spec.Service.ProxySecretRef
	ociRepository.Spec.Interval = release.Spec.Service.Interval
	ociRepository.Spec.Timeout = release.Spec.Service.Timeout
	ociRepository.Spec.Ignore = release.Spec.Service.Ignore
	ociRepository.Spec.Insecure = release.Spec.Service.Insecure
	// TODO: Check this with Release.Spec.suspended
	ociRepository.Spec.Suspend = release.Spec.Service.Suspend
	// TODO: Patch with url rewriters

}

func (r *ReleaseReconciler) createOciRepository(op *operation) error {
	ociRepository := &sourcev1b2.OCIRepository{}
	ociRepository.SetName(op.release.Name)
	ociRepository.SetNamespace(op.release.Namespace)
	populateOciRepository(ociRepository, op.release)
	err := ctrl.SetControllerReference(op.release, ociRepository, r.Scheme)
	if err != nil {
		return fmt.Errorf("unable to set OCI Repository owner reference: %w", err)
	}
	if err = r.Create(op.ctx, ociRepository); err != nil {
		return fmt.Errorf("error while creating associated OCI repository: %w", err)
	}
	return nil
}

func (r *ReleaseReconciler) patchOciRepository(op *operation, ociRepository *sourcev1b2.OCIRepository) (bool, error) {
	originalGeneration := ociRepository.Generation
	patch := client.MergeFrom(ociRepository.DeepCopy())
	populateOciRepository(ociRepository, op.release)
	err := r.Patch(op.ctx, ociRepository, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching OCI repository: %w", err)
	}
	return originalGeneration != ociRepository.Generation, nil
}

func buildConditionStatusByType(conditions []metav1.Condition, logger logr.Logger) map[string]metav1.ConditionStatus {
	statusByType := make(map[string]metav1.ConditionStatus)
	if len(conditions) < 2 {
		logger.V(0).Info("Not enough conditions found yet")
	}
	for _, condition := range conditions {
		logger.V(1).Info("OCI Repository condition", "type", condition.Type, "status", condition.Status)
		statusByType[condition.Type] = condition.Status
	}
	return statusByType
}
