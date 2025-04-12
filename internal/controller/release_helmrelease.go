package controller

import (
	"fmt"
	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func (r *ReleaseReconciler) handleHelmRelease(op *releaseOperation, rendered *kubopackage.Rendered, name string, module *kubopackage.Module) (*fluxv2.HelmRelease, ReconcileError) {
	enabled := rendered.ModuleRenderedByName[module.Name].Enabled

	helmRelease := &fluxv2.HelmRelease{}
	err := r.Get(op.ctx, types.NamespacedName{Name: name, Namespace: op.release.Namespace}, helmRelease)
	if err != nil {
		//logger.V(1).Info("Unable to fetch helmRelease", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on helmRelease '%s': %w", name, err), false, "HelmReleaseAccess")
		}
		if enabled {
			// Must create it
			op.logger.V(0).Info("Will create helmRelease", "name", name, "namespace", op.release.Namespace, "module", module.Name)
			err := r.createHelmRelease(op, rendered, name, module)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleaseCreate")
			}
			r.Event(op.release, "Normal", "HelmReleaseCreated", fmt.Sprintf("Created HelmRelease %q", name))
			op.logger.V(1).Info("Launched helmRelease", "helmReleaseName", name)
			op.helmReleaseStates[module.Name] = kv1alpha1.HelmReleaseState{
				Ready:  metav1.ConditionUnknown,
				Status: "",
			}
			return helmRelease, nil
		} else {
			op.logger.V(1).Info("Disabled helmRelease", "helmReleaseName", name)
			delete(op.helmReleaseStates, module.Name)
			// Nothing to do.
			return nil, nil
		}
	} else {
		if enabled {
			changed, err := r.patchHelmRelease(op, helmRelease, rendered, module)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleasePatch")
			}
			if changed {
				op.logger.V(0).Info("HelmRelease updated", "name", name, "namespace", op.release.Namespace, "module", module.Name)
			} else {
				op.logger.V(1).Info("HelmRelease unchanged", name, "namespace", op.release.Namespace, "module", module.Name)
			}
			op.helmReleaseStates[module.Name] = computeHelmReleaseState(helmRelease)
			return helmRelease, nil
		} else {
			// Must delete
			err := r.Delete(op.ctx, helmRelease)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleaseDelete")
			}
			op.logger.V(1).Info("Delete helmRelease", "helmReleaseName", name)
			delete(op.helmReleaseStates, module.Name)
			return nil, nil
		}
	}
}

func computeHelmReleaseState(helmRelease *fluxv2.HelmRelease) kv1alpha1.HelmReleaseState {
	result := kv1alpha1.HelmReleaseState{}
	for _, condition := range helmRelease.Status.Conditions {
		if condition.Type == "Ready" {
			result.Ready = condition.Status
		}
		if condition.Type == "Released" {
			result.Status = condition.Message
		}
	}
	return result
}

func PopulateHelmRelease(
	helmRelease *fluxv2.HelmRelease,
	release *kv1alpha1.Release,
	pckContainer *kubopackage.PckContainer,
	rendered *kubopackage.Rendered,
	helmRepositoryName string,
	module *kubopackage.Module,
	helmReleaseNameByModuleName map[string]string,
) {
	helmRelease.Spec.Interval = release.Spec.Package.Interval
	chartRef, ok := pckContainer.Status.ChartByModule[module.Name]
	if !ok {
		panic("Internal error chart not found by module name")
	}
	moduleRendered := rendered.ModuleRenderedByName[module.Name]

	dependsOn := make([]map[string]string, 0)
	for _, dep := range moduleRendered.DependsOn {
		rn, ok := helmReleaseNameByModuleName[dep]
		if !ok {
			// Should no occurs, as this should be trapped on module rendering
			panic(fmt.Sprintf("dependency '%s' not found for module name '%s'", dep, module.Name))
		}
		dependsOn = append(dependsOn, map[string]string{
			"name":      rn,
			"namespace": helmRelease.Namespace, // All helmRelease of a release are in the same namespace
		})
	}
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
				"interval": release.Spec.Package.Interval,
			},
		},
		"values":          moduleRendered.Values,
		"targetNamespace": moduleRendered.TargetNamespace,
		"releaseName":     helmRelease.Name, // We remove namespace from the releaseName
		"dependsOn":       dependsOn,
	}
	spec = misc.MergeMaps(spec, moduleRendered.SpecPatch)
	patch, ok := release.Spec.SpecPatchByModule[module.Name]
	if ok {
		var err error
		spec, err = Merge(spec, patch)
		if err != nil {
			panic(err)
		}
	}
	specTxt, err := yaml.Marshal(spec)
	if err != nil {
		panic(err)
	}
	// fmt.Printf("================= specTxt\n%s\n", specTxt)
	err = yaml.Unmarshal(specTxt, &helmRelease.Spec)
	if err != nil {
		panic(err)
	}
}

func (r *ReleaseReconciler) createHelmRelease(op *releaseOperation, rendered *kubopackage.Rendered, name string, module *kubopackage.Module) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(name)
	helmRelease.SetNamespace(op.release.Namespace)
	PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName)
	err := ctrl.SetControllerReference(op.release, helmRelease, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", name, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %w", name, err)
	}
	return nil
}

func (r *ReleaseReconciler) patchHelmRelease(op *releaseOperation, helmRelease *fluxv2.HelmRelease, rendered *kubopackage.Rendered, module *kubopackage.Module) (bool, error) {
	originalGeneration := helmRelease.Generation
	patch := client.MergeFrom(helmRelease.DeepCopy())
	PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName)
	err := r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %w", helmRelease.Name, err)
	}
	return originalGeneration != helmRelease.Generation, nil
}
