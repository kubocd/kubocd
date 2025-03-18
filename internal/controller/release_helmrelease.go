package controller

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/application"
	"kubocd/internal/misc"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func (r *ReleaseReconciler) handleHelmRelease(op *releaseOperation, rendered *application.Rendered, name, moduleName string) (*fluxv2.HelmRelease, ReconcileError) {
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
		err := r.createHelmRelease(op, rendered, name, moduleName)
		if err != nil {
			return nil, NewReconcileError(err, false, "HelmReleaseCreate")
		}
		r.Event(op.release, "Normal", "HelmReleaseCreated", fmt.Sprintf("Created HelmRelease %q", op.release.Name))
		return helmRelease, nil
	} else {
		changed, err := r.patchHelmRelease(op, helmRelease, rendered, moduleName)
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

func PopulateHelmRelease(helmRelease *fluxv2.HelmRelease, release *kv1alpha1.Release, appContainer *application.AppContainer, rendered *application.Rendered, helmRepositoryName string, moduleName string) {
	helmRelease.Spec.Interval = release.Spec.Application.Interval
	chartRef, ok := appContainer.Status.ChartByModule[moduleName]
	if !ok {
		panic("Internal error chart not found by module name")
	}
	moduleRendered := rendered.ModuleRenderedByName[moduleName]

	spec := map[string]interface{}{
		"chart": map[string]interface{}{
			"spec": map[string]interface{}{
				"chart":   chartRef.Name,
				"version": chartRef.Version,
				"sourceRef": map[string]interface{}{
					"kind":      "HelmRepository",
					"name":      helmRepositoryName,
					"namespace": release.Namespace,
				},
				"interval": release.Spec.Application.Interval,
			},
		},
		"values":          moduleRendered.Values,
		"targetNamespace": moduleRendered.Namespace,
	}
	spec = misc.MergeMaps(spec, moduleRendered.Config)
	specTxt, err := yaml.Marshal(spec)
	if err != nil {
		panic(err)
	}
	fmt.Printf("================= specTxt\n%s\n", specTxt)
	err = yaml.Unmarshal(specTxt, &helmRelease.Spec)
	if err != nil {
		panic(err)
	}
	//helmRelease.Spec.Chart = &fluxv2.HelmChartTemplate{
	//	Spec: fluxv2.HelmChartTemplateSpec{
	//		Chart:   chartRef.Name,
	//		Version: chartRef.Version,
	//		SourceRef: fluxv2.CrossNamespaceObjectReference{
	//			Kind:      "HelmRepository",
	//			Name:      helmRepositoryName,
	//			Namespace: release.Namespace,
	//		},
	//		Interval: &release.Spec.Application.Interval,
	//	},
	//}
}

func (r *ReleaseReconciler) createHelmRelease(op *releaseOperation, rendered *application.Rendered, name string, moduleName string) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(name)
	helmRelease.SetNamespace(op.release.Namespace)
	PopulateHelmRelease(helmRelease, op.release, op.appContainer, rendered, op.helmRepositoryName, moduleName)
	err := ctrl.SetControllerReference(op.release, helmRelease, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", name, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %w", name, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchHelmRelease(op *releaseOperation, helmRelease *fluxv2.HelmRelease, rendered *application.Rendered, moduleName string) (bool, error) {
	originalGeneration := helmRelease.Generation
	patch := client.MergeFrom(helmRelease.DeepCopy())
	PopulateHelmRelease(helmRelease, op.release, op.appContainer, rendered, op.helmRepositoryName, moduleName)
	err := r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %w", helmRelease.Name, err)
	}
	return originalGeneration != helmRelease.Generation, nil
}
