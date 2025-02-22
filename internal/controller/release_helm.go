package controller

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kubocdv1alpha1 "kubocd/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleHelmRelease(op *operation, moduleName string) (*fluxv2.HelmRelease, *ReconcileError) {
	name := fmt.Sprintf("%s-%s", op.release.Name, moduleName)
	// Fetch associated OCIRepository
	helmRelease := &fluxv2.HelmRelease{}
	err := r.Get(op.ctx, types.NamespacedName{Name: name, Namespace: op.release.Namespace}, helmRelease)
	if err != nil {
		//logger.V(1).Info("Unable to fetch helmRelease", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on helmRelease '%s': %w", name, err), false, "HelmReleaseAccess")
		}
		// Must create it
		op.logger.V(0).Info("Will create helmRelease", "name", name, "namespace", op.release.Namespace, "module", moduleName)
		err := r.createHelmRelease(op, name)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmReleaseCreate")
		}
		r.Event(op.release, "Normal", "HelmReleaseCreated", fmt.Sprintf("Created HelmRelease %q", op.release.Name))
		return helmRelease, nil
	} else {
		changed, err := r.patchHelmRelease(op, helmRelease)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmReleasePatch")
		}
		if changed {
			op.logger.V(0).Info("HelmRelease updated", "name", name, "namespace", op.release.Namespace, "module", moduleName)
		} else {
			op.logger.V(1).Info("HelmRelease unchanged", name, "namespace", op.release.Namespace, "module", moduleName)
		}
		return helmRelease, nil
	}
}

func (r *ReleaseReconciler) createHelmRelease(op *operation, name string) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(name)
	helmRelease.SetNamespace(op.release.Namespace)
	populateHelmRelease(helmRelease, op.release)
	err := ctrl.SetControllerReference(op.release, helmRelease, r.Scheme)
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", name, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %w", name, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchHelmRelease(op *operation, helmRelease *fluxv2.HelmRelease) (bool, error) {
	originalGeneration := helmRelease.Generation
	patch := client.MergeFrom(helmRelease.DeepCopy())
	populateHelmRelease(helmRelease, op.release)
	err := r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %w", helmRelease.Name, err)
	}
	return originalGeneration != helmRelease.Generation, nil
}

func populateHelmRelease(helmRelease *fluxv2.HelmRelease, release *kubocdv1alpha1.Release) {
	helmRelease.Spec.Interval = release.Spec.Service.Interval
	helmRelease.Spec.ChartRef = &fluxv2.CrossNamespaceSourceReference{
		Kind:      "OCIRepository",
		Name:      helmRelease.Name, // WE ASSUME NAMES ARE SAME FOR HelmRelease and associated OCIRepository
		Namespace: release.Namespace,
	}
}
