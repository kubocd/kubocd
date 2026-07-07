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

// This file host helper function for release controller.
// Most of these function are also used by the 'render' CLI command.
// This is why they are not implemented as method of the reconciler

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func HandleParameters(release *kv1alpha1.Release, kcontext map[string]interface{}, configStore configstore.ConfigStore, pckContainer *kubopackage.PckContainer) (map[string]interface{}, error) {

	var parametersStr string
	if release.Spec.Parameters == nil || release.Spec.Parameters.Raw == nil || len(release.Spec.Parameters.Raw) == 0 {
		parametersStr = "{}"
		//return pckContainer.DefaultParameters, nil
	} else {
		parametersStr = string(release.Spec.Parameters.Raw)
	}

	var err error
	if strings.Contains(parametersStr, "\\n") && parametersStr[0:1] != "{" { // If there is some '\n' and this is not JSON.
		parametersStr, err = strconv.Unquote(parametersStr)
		if err != nil {
			return nil, fmt.Errorf("could not unquote parameter value: %w", err)
		}
	}

	parametersTmpl, err := tmpl.NewFromAny("", parametersStr, "")
	if err != nil {
		return nil, fmt.Errorf("could not create template from parameters: %w", err)
	}
	pModel := BuildModel(kcontext, nil, release, configStore)

	parameters, txt, err := parametersTmpl.RenderToMap(pModel)
	if err != nil {
		return nil, fmt.Errorf("could not render parameters template: %w (%s)", err, txt)
	}

	parameters = misc.MergeMaps(pckContainer.DefaultParameters, parameters)
	err = pckContainer.ValidateParameters(parameters)
	if err != nil {
		return nil, fmt.Errorf("could not validate parameters: %w", err)
	}
	return parameters, nil
}

func BuildHelmReleaseName(releaseName, moduleName string) string {
	if moduleName == "noname" {
		return releaseName
	}
	return fmt.Sprintf(HelmReleaseNameFormat, releaseName, moduleName)
}

func computeReadyReleases(op *releaseOperation) (str string, allReady bool) {
	cnt := 0
	for _, releaseState := range op.helmReleaseStates {
		if releaseState.Ready == metav1.ConditionTrue {
			cnt++
		}
	}
	return fmt.Sprintf("%d/%d", cnt, len(op.helmReleaseStates)), cnt == len(op.helmReleaseStates)
}

// ComputeContext is aimed to be called by this reconciler, but also by the render CLI command
func ComputeContext(ctx context.Context, k8sClient client.Client, release *kv1alpha1.Release, store configstore.ConfigStore, defaultContext map[string]interface{}) (map[string]interface{}, []kv1alpha1.NamespacedName, ReconcileError) {

	optionalContexts := make(map[kv1alpha1.NamespacedName]bool)

	contextList := make([]kv1alpha1.NamespacedName, 0, 3)
	if !release.Spec.SkipDefaultContext {
		contextList = append(contextList, store.GetDefaultContexts()...)
		for _, nsContextName := range store.GetDefaultNamespaceContexts() {
			nsContext := kv1alpha1.NamespacedName{
				Namespace: release.GetNamespace(),
				Name:      nsContextName,
			}
			contextList = append(contextList, nsContext)
			optionalContexts[nsContext] = true
		}
	}
	contextList = append(contextList, release.Spec.Contexts...)
	effectiveContextList := make([]kv1alpha1.NamespacedName, 0, len(contextList))
	resultContext := defaultContext
	for _, contextRef := range contextList {
		contextObj := &kv1alpha1.Context{}
		err := k8sClient.Get(ctx, contextRef.ToObjectKey(), contextObj)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				if optionalContexts[contextRef] {
					continue // This specific context may not exist. This is not an error
				} else {
					return nil, nil, NewReconcileError(fmt.Errorf("context '%s' not found", contextRef.String()), true, "ContextNotFound")
				}
			} else {
				return nil, nil, NewReconcileError(err, false, "ContextRetrieval")
			}
		}
		if contextObj.Status.Phase != kv1alpha1.ContextPhaseReady {
			return nil, nil, NewReconcileError(fmt.Errorf("context '%s' is in error", contextRef.String()), true, "ContextRetrieval")
		}
		// OK. Merge our info on top
		ctx := contextObj.Status.Context
		if ctx == nil {
			ctx = contextObj.Spec.Context
		}
		resultContext, err = Merge(resultContext, ctx)
		if err != nil {
			return nil, nil, NewReconcileError(fmt.Errorf("unable to merge context: %w", err), true, "ContextMerge")
		}
		effectiveContextList = append(effectiveContextList, contextRef)
	}
	return resultContext, effectiveContextList, nil
}

func buildConditionStatusByType(conditions []metav1.Condition, repoKind string, repoName string, logger logr.Logger) map[string]metav1.ConditionStatus {
	statusByType := make(map[string]metav1.ConditionStatus)
	if len(conditions) < 2 {
		logger.V(1).Info("Not enough conditions found yet", repoKind, repoName)
	}
	for _, condition := range conditions {
		logger.V(1).Info("condition", "type", condition.Type, "status", condition.Status, repoKind, repoName)
		statusByType[condition.Type] = condition.Status
	}
	return statusByType
}

// GroomRelease is aimed to be called by this reconciler, but also by the render CLI command
func GroomRelease(release *kv1alpha1.Release, logger logr.Logger, configStore configstore.ConfigStore) {
	if release.Spec.TargetNamespace == "" {
		release.Spec.TargetNamespace = release.Namespace
	}
	//if release.Spec.Timeout == nil {
	//	dht := metav1.Duration{Duration: store.GetDefaultHelmTimeout()}
	//	release.Spec.Timeout = &dht
	//}
	if release.Spec.Contexts == nil {
		release.Spec.Contexts = make([]kv1alpha1.NamespacedName, 0)
	}
	if release.Spec.Roles == nil {
		release.Spec.Roles = make([]string, 0)
	}
	if release.Spec.Dependencies == nil {
		release.Spec.Dependencies = make([]string, 0)
	}
	if release.Spec.Debug == nil {
		release.Spec.Debug = &kv1alpha1.ReleaseDebug{}
	}
	if misc.IsZero(release.Spec.Package.Interval) {
		release.Spec.Package.Interval = metav1.Duration{
			Duration: configStore.GetDefaultPackageInterval(),
		}
	}
	for i := range release.Spec.Contexts {
		kCtx := &release.Spec.Contexts[i]
		if kCtx.Namespace == "" {
			logger.V(1).Info("Set namespace for context", "contextName", kCtx.Name, "contextNamespace", release.ObjectMeta.Namespace)
			kCtx.Namespace = release.ObjectMeta.Namespace
		}
	}
}

func BuildModel(context map[string]interface{}, parameters map[string]interface{}, release *kv1alpha1.Release, store configstore.ConfigStore) map[string]interface{} {
	model := map[string]interface{}{
		"Context":         context,
		"Parameters":      parameters,
		"Release":         misc.ObjectToMap(release),
		"ImageRedirector": store,
	}
	return model
}

// BuildInputModel build the '.Inputs' in the data model for rendering values.
// return:
// - inputModel: The map to be inserted in the data model
// - inputConnections: A list of the input connection, used to managed reconciliation triggering.
// - missingInputs: A list of missing connection, to be set in status to human display
// - err:
func BuildInputModel(k8sClient client.Client, inputs []kubopackage.InputRendered, defaultNamespace string) (inputModel map[string]interface{}, inputConnections []kv1alpha1.ReleaseInputConnection, missingInputs string, err error) {
	inputModel = map[string]interface{}{}
	inputConnections = make([]kv1alpha1.ReleaseInputConnection, len(inputs))
	missingInputList := make([]string, 0)
	for idx, input := range inputs {
		if input.Connection.Namespace == "" {
			input.Connection.Namespace = defaultNamespace
		}
		if input.Alias == "" {
			input.Alias = input.Iface
		}
		connection := &kv1alpha1.Connection{}
		if input.Connection.Name != "" {
			// User target an unmanaged connection. Just read it
			nsName := types.NamespacedName{Namespace: input.Connection.Namespace, Name: input.Connection.Name}
			// We set in the status list even if not found or in error. As we want to be notified if created.
			inputConnections[idx] = kv1alpha1.ReleaseInputConnection{
				Name:      nsName.Name,
				Namespace: nsName.Namespace,
			}
			err := k8sClient.Get(context.Background(), nsName, connection)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					missingInputList = append(missingInputList, nsName.String())
					continue
				}
				return nil, nil, "", fmt.Errorf("could not get connection '%s': %w", nsName.String(), err)
			}
			if connection.Status.Phase != kv1alpha1.ConnectionPhaseReady {
				missingInputList = append(missingInputList, nsName.String())
				continue
			}
			values := make(map[string]interface{})
			err = yaml.UnmarshalStrict(connection.Spec.Values.Raw, &values)
			if err != nil {
				return nil, nil, "", fmt.Errorf("could not unmarshal connection '%s' values: %w", nsName.String(), err)
			}
			inputModel[input.Alias] = values
		} else {
			// TODO: Lookup connection based on interface
			return nil, nil, "", fmt.Errorf("managed connection not yet implemented")
		}
	}
	if len(missingInputList) > 0 {
		missingInputs = strings.Join(missingInputList, ",")
	}
	return // inputModel, inputConnections, missingInputs, nil
}
