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
			return nil, NewReconcileError(fmt.Errorf("could not get HelmRelease '%s': %s", helmReleaseName, apiError(err)), false, "HelmReleaseAccess")
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
				op.logger.V(1).Info("HelmRelease unchanged", "name", helmReleaseName, "namespace", op.release.Namespace, "module", module.Name)
			}
			op.helmReleaseStates[module.Name] = computeHelmReleaseState(helmRelease)
			return helmRelease, nil
		} else {
			// Must delete
			err := r.Delete(op.ctx, helmRelease)
			if err != nil {
				return nil, NewReconcileError(fmt.Errorf("error while deleting HelmRelease '%s': %s", helmReleaseName, apiError(err)), false, "HelmReleaseDelete")
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
	configStore configstore.ConfigStore,
) error {
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
	for idx, dep := range moduleRendered.DependsOn {
		rn, ok := helmReleaseNameByModuleName[dep]
		if !ok {
			return fmt.Errorf("dependsOn[%d] not found for module '%s'", idx, module.Name)
		}
		dependsOn = append(dependsOn, map[string]string{
			"name":      rn,
			"namespace": helmRelease.Namespace, // All helmRelease of a release are in the same namespace
		})
	}

	spec, err := helmReleaseSpec(helmRelease, release, chartRef, moduleRendered, dependsOn, helmRepositoryName, module, configStore,
		moduleRendered.SpecPatch)
	if err != nil {
		return err
	}
	if err := decodeHelmReleaseSpec(spec, &helmRelease.Spec); err != nil {
		// Checked on failure only, as a later patch can replace what specPatch sets
		withoutSpecPatch, buildErr := helmReleaseSpec(helmRelease, release, chartRef, moduleRendered, dependsOn, helmRepositoryName,
			module, configStore, nil)
		if buildErr == nil && decodeHelmReleaseSpec(withoutSpecPatch, &fluxv2.HelmReleaseSpec{}) == nil {
			return fmt.Errorf("specPatch of module '%s' has %w", module.Name, err)
		}
		return fmt.Errorf("failed to unmarshal spec into HelmRelease for module '%s': the spec merged from the package, the "+
			"configuration and the Release has %w", module.Name, err)
	}
	return nil
}

// helmReleaseSpec merges the spec of the HelmRelease of module, in this order:
// the chart, the rendered values and settings of the module, the failure
// strategies and the patch of the configuration, specPatch, then the
// overrides of the Release.
func helmReleaseSpec(
	helmRelease *fluxv2.HelmRelease,
	release *kv1alpha1.Release,
	chartRef kubopackage.ChartRef,
	moduleRendered *kubopackage.ModuleRendered,
	dependsOn []map[string]string,
	helmRepositoryName string,
	module *kubopackage.Module,
	configStore configstore.ConfigStore,
	specPatch map[string]interface{},
) (map[string]interface{}, error) {
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
		"interval":        &moduleRendered.Interval,
	}

	// --------------- Apply DefaultOnFailureStrategy if any
	defaultStrategy := configStore.GetDefaultOnFailureStrategy()
	if defaultStrategy != nil {
		spec = misc.MergeMaps(spec, defaultStrategy)
	}
	// ------------------------- Patch helmRelease OnFailureStrategy from the package
	if moduleRendered.OnFailureStrategy != "" {
		strategy := configStore.GetOnFailureStrategy(moduleRendered.OnFailureStrategy)
		if strategy == nil {
			return nil, fmt.Errorf("onFailureStrategy of module '%s' is not a known strategy", module.Name)
		}
		deleteFailureConf(spec)
		spec = misc.MergeMaps(spec, strategy)
	}
	// ------------------------------------------------ Apply global and from package patch
	var err error
	spec, err = Merge(spec, configStore.GetSpecPatch())
	if err != nil {
		return nil, fmt.Errorf("unable to merge config.specPatch on module '%s': %w", module.Name, err)
	}
	spec = misc.MergeMaps(spec, specPatch) // Including install.namespace

	var override *kv1alpha1.ModuleOverrideSpec = nil
	// ------------------------------------------------ Handle module override
	if release.Spec.ModuleOverrides != nil {
		for idx, ovr := range release.Spec.ModuleOverrides {
			if ovr.Module == module.Name {
				if override == nil {
					override = &release.Spec.ModuleOverrides[idx]
				} else {
					return nil, fmt.Errorf("override for module '%s' defined twice", module.Name)
				}
			}
		}
	}
	if override != nil {
		if override.OnFailureStrategy != "" {
			onFailureStrategy := configStore.GetOnFailureStrategy(override.OnFailureStrategy)
			if onFailureStrategy == nil {
				return nil, fmt.Errorf("onFailureStrategyByModule: '%s' not found for module '%s'", override.OnFailureStrategy, module.Name)
			}
			deleteFailureConf(spec)
			spec = misc.MergeMaps(spec, onFailureStrategy)
		}
		// ------------------------- And handle generic specPatchByModule
		if override.SpecPatch != nil {
			var err error
			spec, err = Merge(spec, override.SpecPatch)
			if err != nil {
				return nil, fmt.Errorf("failed to merge spec patch for module '%s': %w", module.Name, err)
			}
		}
		if !misc.IsZero(override.Timeout) {
			spec["timeout"] = override.Timeout
		}
		if !misc.IsZero(override.Interval) {
			spec["interval"] = override.Interval
		}
	}
	return spec, nil
}

var (
	errUndefinedField = errors.New("a field that HelmRelease does not define")
	errWrongValue     = errors.New("a value of the wrong type or format")
)

// decodeHelmReleaseSpec decodes spec into target, rejecting the fields that
// HelmRelease does not define. Its error is a category: the decoder message
// quotes the field or the value at fault.
func decodeHelmReleaseSpec(spec map[string]interface{}, target *fluxv2.HelmReleaseSpec) error {
	specTxt, err := yaml.Marshal(spec)
	if err != nil {
		return errWrongValue
	}
	if yaml.UnmarshalStrict(specTxt, target) == nil {
		return nil
	}
	if yaml.Unmarshal(specTxt, &fluxv2.HelmReleaseSpec{}) == nil {
		return errUndefinedField
	}
	return errWrongValue
}

func deleteFailureConf(spec map[string]interface{}) {
	install, ok := spec["install"].(map[string]interface{})
	if ok {
		_, ok := install["strategy"].(map[string]interface{})
		if ok {
			delete(install, "strategy")
		}
		_, ok = install["remediation"].(map[string]interface{})
		if ok {
			delete(install, "remediation")
		}
	}
	upgrade, ok := spec["upgrade"].(map[string]interface{})
	if ok {
		_, ok := upgrade["strategy"].(map[string]interface{})
		if ok {
			delete(upgrade, "strategy")
		}
		_, ok = upgrade["remediation"].(map[string]interface{})
		if ok {
			delete(upgrade, "remediation")
		}
	}
}

func (r *ReleaseReconciler) createHelmRelease(op *releaseOperation, rendered *kubopackage.Rendered, helmReleaseName string, module *kubopackage.Module) error {
	helmRelease := &fluxv2.HelmRelease{}
	helmRelease.SetName(helmReleaseName)
	helmRelease.SetNamespace(op.release.Namespace)
	err := PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName, r.ConfigStore)
	if err != nil {
		return fmt.Errorf("failed to populate HelmRelease '%s': %w", helmReleaseName, err)
	}
	err = ctrl.SetControllerReference(op.release, helmRelease, r.Scheme())
	if err != nil {
		return fmt.Errorf("unable to set HelmRelease '%s' owner reference: %w", helmReleaseName, err)
	}
	if err = r.Create(op.ctx, helmRelease); err != nil {
		return fmt.Errorf("error while creating HelmRelease '%s': %s", helmReleaseName, apiError(err))
	}
	return nil
}

func patchHelmRelease(r *ReleaseReconciler, op *releaseOperation, helmRelease *fluxv2.HelmRelease, rendered *kubopackage.Rendered, module *kubopackage.Module) (bool, error) {
	// Store original generation to detect changes
	originalGeneration := helmRelease.Generation

	// Create a deep copy for the patch operation
	patch := client.MergeFrom(helmRelease.DeepCopy())

	// Populate the HelmRelease with updated configuration
	err := PopulateHelmRelease(helmRelease, op.release, op.pckContainer, rendered, op.helmRepositoryName, module, op.helmReleaseNameByModuleName, r.ConfigStore)
	if err != nil {
		return false, fmt.Errorf("failed to populate HelmRelease '%s': %w", helmRelease.Name, err)
	}

	// Apply the patch
	err = r.Patch(op.ctx, helmRelease, patch)
	if err != nil {
		return false, fmt.Errorf("error while patching HelmRelease '%s': %s", helmRelease.Name, apiError(err))
	}

	// Check if the generation changed to determine if an update occurred
	return originalGeneration != helmRelease.Generation, nil
}

func BuildHelmReleaseName(releaseName, moduleName string) string {
	if moduleName == "noname" {
		return releaseName
	}
	return fmt.Sprintf(HelmReleaseNameFormat, releaseName, moduleName)
}

func computeReadyHelmReleases(op *releaseOperation) (str string, allReady bool) {
	cnt := 0
	for _, releaseState := range op.helmReleaseStates {
		if releaseState.Ready == metav1.ConditionTrue {
			cnt++
		}
	}
	return fmt.Sprintf("%d/%d", cnt, len(op.helmReleaseStates)), cnt == len(op.helmReleaseStates)
}
