package controller

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ReleaseReconciler) handleHelmRelease(op *releaseOperation, name, moduleName string) (*fluxv2.HelmRelease, ReconcileError) {
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
		err := r.createHelmRelease(op, name, moduleName)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmReleaseCreate")
		}
		r.Event(op.release, "Normal", "HelmReleaseCreated", fmt.Sprintf("Created HelmRelease %q", op.release.Name))
		return helmRelease, nil
	} else {
		changed, err := r.patchHelmRelease(op, helmRelease, moduleName)
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

func populateHelmRelease(helmRelease *fluxv2.HelmRelease, op *releaseOperation, moduleName string) {
	helmRelease.Spec.Interval = op.release.Spec.Application.Interval
	chartRef, ok := op.appContainer.Status.ChartByModule[moduleName]
	if !ok {
		panic("Internal error chart not found by module name")
	}
	helmRelease.Spec.Chart = &fluxv2.HelmChartTemplate{
		Spec: fluxv2.HelmChartTemplateSpec{
			Chart:   chartRef.Name,
			Version: chartRef.Version,
			SourceRef: fluxv2.CrossNamespaceObjectReference{
				Kind:      "HelmRepository",
				Name:      op.helmRepositoryName,
				Namespace: op.release.Namespace,
			},
			Interval: &op.release.Spec.Application.Interval,
		},
	}
}

func (r *ReleaseReconciler) createHelmRelease(op *releaseOperation, name string, moduleName string) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(name)
	helmRelease.SetNamespace(op.release.Namespace)
	populateHelmRelease(helmRelease, op, moduleName)
	err := ctrl.SetControllerReference(op.release, helmRelease, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", name, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %w", name, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchHelmRelease(op *releaseOperation, helmRelease *fluxv2.HelmRelease, moduleName string) (bool, error) {
	originalGeneration := helmRelease.Generation
	patch := client.MergeFrom(helmRelease.DeepCopy())
	populateHelmRelease(helmRelease, op, moduleName)
	err := r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %w", helmRelease.Name, err)
	}
	return originalGeneration != helmRelease.Generation, nil
}
