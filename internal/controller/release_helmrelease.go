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
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func (r *ReleaseReconciler) handleHelmRelease(op *releaseOperation, rendered *kubopackage.Rendered, helmReleaseName string, module *kubopackage.Module) (*fluxv2.HelmRelease, ReconcileError) {
	enabled := rendered.ModuleRenderedByName[module.Name].Enabled

	helmRelease := &fluxv2.HelmRelease{}
	err := r.Get(op.ctx, types.NamespacedName{Name: helmReleaseName, Namespace: op.release.Namespace}, helmRelease)
	if err != nil {
		//logger.V(1).Info("Unable to fetch helmRelease", "error", err.Error())
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("on helmRelease '%s': %w", helmReleaseName, err), false, "HelmReleaseAccess")
		}
		if enabled {
			// Must create it
			op.logger.V(0).Info("Will create helmRelease", "name", helmReleaseName, "namespace", op.release.Namespace, "module", module.Name)
			err := r.createHelmRelease(op, rendered, helmReleaseName, module)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleaseCreate")
			}
			r.Event(op.release, "Normal", "HelmReleaseCreated", fmt.Sprintf("Created HelmRelease %q", helmReleaseName))
			op.logger.V(1).Info("Launched helmRelease", "helmReleaseName", helmReleaseName)
			op.helmReleaseStates[module.Name] = kv1alpha1.HelmReleaseState{
				Ready:  metav1.ConditionUnknown,
				Status: "",
			}
			return helmRelease, nil
		} else {
			op.logger.V(1).Info("Disabled helmRelease", "helmReleaseName", helmReleaseName)
			delete(op.helmReleaseStates, module.Name)
			// Nothing to do.
			return nil, nil
		}
	} else {
		if enabled {
			changed, err := patchHelmRelease(r, op, helmRelease, rendered, module)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleasePatch")
			}
			if changed {
				op.logger.V(0).Info("HelmRelease updated", "name", helmReleaseName, "namespace", op.release.Namespace, "module", module.Name)
			} else {
				op.logger.V(1).Info("HelmRelease unchanged", helmReleaseName, "namespace", op.release.Namespace, "module", module.Name)
			}
			op.helmReleaseStates[module.Name] = computeHelmReleaseState(helmRelease)
			return helmRelease, nil
		} else {
			// Must delete
			err := r.Delete(op.ctx, helmRelease)
			if err != nil {
				return nil, NewReconcileError(err, false, "HelmReleaseDelete")
			}
			op.logger.V(1).Info("Delete helmRelease", "helmReleaseName", helmReleaseName)
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
) error {
	helmRelease.Spec.Interval = release.Spec.Package.Interval

	// Get chart reference for the module
	chartRef, ok := pckContainer.Status.ChartByModule[module.Name]
	if !ok {
		return fmt.Errorf("chart not found for module '%s'", module.Name)
	}

	// Get rendered module configuration
	moduleRendered, ok := rendered.ModuleRenderedByName[module.Name]
	if !ok {
		return fmt.Errorf("rendered module not found for module '%s'", module.Name)
	}

	// Build dependencies list
	dependsOn := make([]map[string]string, 0, len(moduleRendered.DependsOn))
	for _, dep := range moduleRendered.DependsOn {
		rn, ok := helmReleaseNameByModuleName[dep]
		if !ok {
			return fmt.Errorf("dependency '%s' not found for module '%s'", dep, module.Name)
		}
		dependsOn = append(dependsOn, map[string]string{
			"name":      rn,
			"namespace": helmRelease.Namespace, // All helmRelease of a release are in the same namespace
		})
	}

	// Build the spec configuration
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
		"timeout":         &moduleRendered.Timeout,
	}

	// Apply module-specific spec patches
	spec = misc.MergeMaps(spec, moduleRendered.SpecPatch)

	// Apply release-specific spec patches
	patch, ok := release.Spec.SpecPatchByModule[module.Name]
	if ok {
		var err error
		spec, err = Merge(spec, patch)
		if err != nil {
			return fmt.Errorf("failed to merge spec patch for module '%s': %w", module.Name, err)
		}
	}

	// Convert spec to YAML and unmarshal into HelmRelease spec
	specTxt, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal spec to YAML for module '%s': %w", module.Name, err)
	}

	//err = yaml.Unmarshal(specTxt, &helmRelease.Spec)
	//fmt.Printf("HelmRelease spec:\n%s\n", string(specTxt))
	err = yaml.UnmarshalStrict(specTxt, &helmRelease.Spec)
	if err != nil {
		return fmt.Errorf("failed to unmarshal spec into HelmRelease for module '%s': %w", module.Name, err)
	}

	return nil
}

func (r *ReleaseReconciler) createHelmRelease(op *releaseOperation, rendered *kubopackage.Rendered, helmReleaseName string, module *kubopackage.Module) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(helmReleaseName)
	helmRelease.SetNamespace(op.release.Namespace)
	err := PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName)
	if err != nil {
		return fmt.Errorf("failed to populate HelmRelease '%s': %w", helmReleaseName, err)
	}
	err = ctrl.SetControllerReference(op.release, helmRelease, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", helmReleaseName, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %w", helmReleaseName, err)
	}
	return nil
}

func patchHelmRelease(r *ReleaseReconciler, op *releaseOperation, helmRelease *fluxv2.HelmRelease, rendered *kubopackage.Rendered, module *kubopackage.Module) (bool, error) {
	// Store original generation to detect changes
	originalGeneration := helmRelease.Generation

	// Create a deep copy for the patch operation
	patch := client.MergeFrom(helmRelease.DeepCopy())

	// Populate the HelmRelease with updated configuration
	err := PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName)
	if err != nil {
		return false, fmt.Errorf("failed to populate HelmRelease '%s': %w", helmRelease.Name, err)
	}

	// Apply the patch
	err = r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %w", helmRelease.Name, err)
	}

	// Check if the generation changed to determine if an update occurred
	return originalGeneration != helmRelease.Generation, nil
}
