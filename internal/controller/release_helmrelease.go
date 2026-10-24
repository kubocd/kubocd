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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"reflect"
	"slices"
	"strings"

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
		if !metav1.IsControlledBy(helmRelease, op.release) {
			if enabled {
				return nil, NewReconcileError(fmt.Errorf("HelmRelease '%s' exists and is not controlled by this Release", helmReleaseName),
					false, "HelmReleaseOwner")
			}
			op.logger.V(0).Info("HelmRelease of a disabled module left in place, as the Release does not control it", "name",
				helmReleaseName, "module", module.Name)
			delete(op.helmReleaseStates, module.Name)
			return nil, nil
		}
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
			err := r.Delete(op.ctx, helmRelease, client.Preconditions{UID: &helmRelease.UID})
			if err != nil {
				return nil, NewReconcileError(fmt.Errorf("error while deleting HelmRelease '%s': %s", helmReleaseName, apiError(err)), false, "HelmReleaseDelete")
			}
			op.logger.V(1).Info("Delete helmRelease", "helmReleaseName", helmReleaseName)
			delete(op.helmReleaseStates, module.Name)
			return nil, nil
		}
	}
}

// computeHelmReleaseState returns the readiness of the HelmRelease and the
// message of its last Helm action. A Ready condition of an older generation of
// the HelmRelease gives Unknown and no message: it is about an older spec. A
// Ready Unknown gives no message either: helm-controller sets it before a
// release action, while Released still holds the message of the previous one,
// and while it waits for the tests of an action.
func computeHelmReleaseState(helmRelease *fluxv2.HelmRelease) kv1alpha1.HelmReleaseState {
	if staleReady(helmRelease.Status.Conditions, helmRelease.Status.ObservedGeneration, helmRelease.Generation) {
		return kv1alpha1.HelmReleaseState{Ready: metav1.ConditionUnknown}
	}
	result := kv1alpha1.HelmReleaseState{}
	message := ""
	for _, condition := range helmRelease.Status.Conditions {
		if condition.Type == "Ready" {
			result.Ready = condition.Status
		}
		if condition.Type == "Released" {
			message = condition.Message
		}
	}
	if result.Ready == metav1.ConditionTrue || result.Ready == metav1.ConditionFalse {
		result.Status = message
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
	// One configuration for the spec, and for the check of its specPatch
	configuration := configStore.Current()
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

	spec, err := helmReleaseSpec(helmRelease, release, chartRef, moduleRendered, dependsOn, helmRepositoryName, module, configuration,
		moduleRendered.SpecPatch)
	if err != nil {
		return err
	}
	current := retainedSpec(&helmRelease.Spec, spec)
	if err := decodeHelmReleaseSpec(spec, current); err != nil {
		// Checked on failure only, as a later patch can replace what specPatch sets
		withoutSpecPatch, buildErr := helmReleaseSpec(helmRelease, release, chartRef, moduleRendered, dependsOn, helmRepositoryName,
			module, configuration, nil)
		if buildErr == nil && decodeHelmReleaseSpec(withoutSpecPatch, &fluxv2.HelmReleaseSpec{}) == nil {
			return fmt.Errorf("specPatch of module '%s' has %w", module.Name, err)
		}
		return fmt.Errorf("failed to unmarshal spec into HelmRelease for module '%s': the spec merged from the package, the "+
			"configuration and the Release has %w", module.Name, err)
	}
	helmRelease.Spec = *current
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
	configuration configstore.Configuration,
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
	defaultStrategy := configuration.GetDefaultOnFailureStrategy()
	if defaultStrategy != nil {
		spec = misc.MergeMaps(spec, defaultStrategy)
	}
	// ------------------------- Patch helmRelease OnFailureStrategy from the package
	if moduleRendered.OnFailureStrategy != "" {
		strategy := configuration.GetOnFailureStrategy(moduleRendered.OnFailureStrategy)
		if strategy == nil {
			return nil, fmt.Errorf("onFailureStrategy of module '%s' is not a known strategy", module.Name)
		}
		deleteFailureConf(spec)
		spec = misc.MergeMaps(spec, strategy)
	}
	// ------------------------------------------------ Apply global and from package patch
	var err error
	spec, err = Merge(spec, configuration.GetSpecPatch())
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
			onFailureStrategy := configuration.GetOnFailureStrategy(override.OnFailureStrategy)
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

// retainedSpec returns a copy of existing that holds only what spec sets, the
// fields of spec resolved as encoding/json does. Decoding spec into it then
// gives, for the fields spec sets, what decoding spec into existing gives, and
// for the others what a new HelmRelease gets. spec.suspend stays as found when
// spec does not set it, as flux suspend sets it on the HelmRelease itself.
func retainedSpec(existing *fluxv2.HelmReleaseSpec, spec map[string]interface{}) *fluxv2.HelmReleaseSpec {
	specTxt, err := yaml.Marshal(spec)
	if err != nil {
		return &fluxv2.HelmReleaseSpec{}
	}
	specJSON, err := yaml.YAMLToJSON(specTxt)
	if err != nil {
		return &fluxv2.HelmReleaseSpec{}
	}
	decoder := json.NewDecoder(bytes.NewReader(specJSON))
	decoder.UseNumber()
	var tree interface{}
	if err := decoder.Decode(&tree); err != nil {
		return &fluxv2.HelmReleaseSpec{}
	}
	object, ok := tree.(map[string]interface{})
	if !ok {
		return &fluxv2.HelmReleaseSpec{}
	}
	kept := existing.DeepCopy()
	suspend := kept.Suspend
	keepSetFields(reflect.ValueOf(kept).Elem(), object)
	if len(fieldKeys(object, "suspend")) == 0 {
		kept.Suspend = suspend
	}
	return kept
}

var jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()

// keepSetFields zeroes, in v, what tree does not set, tree being the JSON that
// encoding/json decodes into v: a field or a map key that tree lacks. A value
// that decodes itself, and a value that tree sets to null or to a value of
// another kind, are kept whole. A list is kept element by element, as
// encoding/json decodes a list into the elements already there.
func keepSetFields(v reflect.Value, tree interface{}) {
	if v.CanAddr() && v.Addr().Type().Implements(jsonUnmarshaler) {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			keepSetFields(v.Elem(), tree)
		}
	case reflect.Struct:
		object, ok := tree.(map[string]interface{})
		if !ok {
			return
		}
		for _, field := range jsonFields(v) {
			keys := fieldKeys(object, field.name)
			if len(keys) == 0 {
				field.value.SetZero()
				continue
			}
			tree := object[keys[0]]
			for _, key := range keys[1:] {
				tree = unionTree(tree, object[key])
			}
			keepSetFields(field.value, tree)
		}
	case reflect.Map:
		object, ok := tree.(map[string]interface{})
		if !ok || v.Type().Key().Kind() != reflect.String {
			return
		}
		for _, key := range v.MapKeys() {
			if _, set := object[key.String()]; !set {
				v.SetMapIndex(key, reflect.Value{})
			}
		}
	case reflect.Slice, reflect.Array:
		list, ok := tree.([]interface{})
		if !ok {
			return
		}
		for i := 0; i < min(v.Len(), len(list)); i++ {
			keepSetFields(v.Index(i), list[i])
		}
	}
}

// unionTree returns a tree that sets what a or b sets, for two members of an
// object that encoding/json decodes into the same field, one after the other.
// A value other than an object or a list keeps the field whole.
func unionTree(a, b interface{}) interface{} {
	aObject, aIsObject := a.(map[string]interface{})
	bObject, bIsObject := b.(map[string]interface{})
	if aIsObject && bIsObject {
		union := make(map[string]interface{}, len(aObject)+len(bObject))
		for key, value := range aObject {
			union[key] = value
		}
		for key, value := range bObject {
			if former, ok := union[key]; ok {
				value = unionTree(former, value)
			}
			union[key] = value
		}
		return union
	}
	aList, aIsList := a.([]interface{})
	bList, bIsList := b.([]interface{})
	if aIsList && bIsList {
		union := make([]interface{}, max(len(aList), len(bList)))
		for i := range union {
			switch {
			case i < len(aList) && i < len(bList):
				union[i] = unionTree(aList[i], bList[i])
			case i < len(aList):
				union[i] = aList[i]
			default:
				union[i] = bList[i]
			}
		}
		return union
	}
	return nil
}

// fieldKeys returns the keys of object that encoding/json decodes into the
// field called name: that name, and the keys equal to it in another case.
func fieldKeys(object map[string]interface{}, name string) []string {
	keys := []string{}
	for key := range object {
		if strings.EqualFold(key, name) {
			keys = append(keys, key)
		}
	}
	return keys
}

type jsonField struct {
	name  string
	value reflect.Value
}

// jsonFields returns the fields of the struct v that encoding/json decodes,
// those of an embedded struct without a name promoted, unless a field of v has
// their name.
func jsonFields(v reflect.Value) []jsonField {
	var fields, promoted []jsonField
	for i := 0; i < v.NumField(); i++ {
		structField := v.Type().Field(i)
		tag := structField.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		value := v.Field(i)
		if structField.Anonymous && name == "" {
			embedded := value
			if embedded.Kind() == reflect.Pointer && !embedded.IsNil() {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				promoted = append(promoted, jsonFields(embedded)...)
				continue
			}
		}
		if !structField.IsExported() {
			continue
		}
		if name == "" {
			name = structField.Name
		}
		fields = append(fields, jsonField{name: name, value: value})
	}
	for _, field := range promoted {
		if !slices.ContainsFunc(fields, func(f jsonField) bool { return f.name == field.name }) {
			fields = append(fields, field)
		}
	}
	return fields
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
	patch := mergeFromWithUID(helmRelease)

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
