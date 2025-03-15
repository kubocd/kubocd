package controller

import (
	"fmt"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleHelmRepository(op *releaseOperation, repoUrl string) (*sourcev1.HelmRepository, ReconcileError) {
	// Fetch associated HelmRepository
	helmRepository := &sourcev1.HelmRepository{}
	err := r.Get(op.ctx, types.NamespacedName{Name: op.helmRepositoryName, Namespace: op.release.Namespace}, helmRepository)
	if err != nil {
		//logger.V(1).Info("Unable to fetch HELM Repository", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on '%s': %w", op.helmRepositoryName, err), false, "HelmRepositoryAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create associated HelmRepository", "name", op.helmRepositoryName, "namespace", op.release.Namespace)
		err := r.createHelmRepository(op, repoUrl)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmRepositoryCreateFailed")
		}
		r.Event(op.release, "Normal", "HelmRepositoryCreated", fmt.Sprintf("Created HelmRepository %q", op.release.Name))
		// Caller will Requeue, waiting for Helm
		return nil, nil
	} else {
		changed, err := r.patchHelmRepository(op, helmRepository, repoUrl)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmRepositoryPatchFailed")
		}
		if changed {
			op.logger.V(0).Info("Helm repository updated", "name", op.helmRepositoryName, "namespace", op.release.Namespace)
		} else {
			op.logger.V(1).Info("Helm repository unchanged", "name", op.ociRepositoryName, "namespace", op.release.Namespace)
		}
	}
	statusByType := buildConditionStatusByType(helmRepository.Status.Conditions, "HelmRepository", op.helmRepositoryName, op.logger)

	if statusByType["Ready"] != metav1.ConditionTrue {
		readyCondition, ok := statusByType["Ready"]
		if !ok || readyCondition == metav1.ConditionUnknown || readyCondition == metav1.ConditionFalse {
			//  Caller will requeue, waiting for Helm
			return nil, nil
		}
		// Something wrong with Helm repo
		return nil, NewReconcileError(fmt.Errorf("invalid status '%s' for Ready condition on HelmRepository '%s'", statusByType["Ready"], op.helmRepositoryName), true, "HelmRepositoryNotReady")
	}
	if helmRepository.Status.Artifact == nil {
		//return nil, NewReconcileError(fmt.Errorf("null status.artifact on HelmRepository '%s'", name), false, "HelmRepositoryNotReady")
		//  Caller will requeue, waiting for Helm
		return nil, nil
	}
	return helmRepository, nil
}

func populateHelmRepository(helmRepository *sourcev1.HelmRepository, op *releaseOperation, repoUrl string) {
	helmRepository.Spec.Interval = op.release.Spec.Application.Interval
	helmRepository.Spec.URL = repoUrl
}

func (r *ReleaseReconciler) createHelmRepository(op *releaseOperation, repoUrl string) error {
	helmRepository := &sourcev1.HelmRepository{}
	helmRepository.SetName(op.helmRepositoryName)
	helmRepository.SetNamespace(op.release.Namespace)
	populateHelmRepository(helmRepository, op, repoUrl)
	err := ctrl.SetControllerReference(op.release, helmRepository, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set owner reference on HelmRepository '%s': %w", op.helmRepositoryName, err)
	}
	if err = r.Create(op.ctx, helmRepository); err != nil {
		return fmt.Errorf("error while creating associated HelmRepository '%s': %w", op.ociRepositoryName, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchHelmRepository(op *releaseOperation, helmRepository *sourcev1.HelmRepository, repoUrl string) (bool, error) {
	originalGeneration := helmRepository.Generation
	patch := client.MergeFrom(helmRepository.DeepCopy())
	populateHelmRepository(helmRepository, op, repoUrl)
	err := r.Patch(op.ctx, helmRepository, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRepository '%s': %w", helmRepository.Name, err)
	}
	return originalGeneration != helmRepository.Generation, nil
}
