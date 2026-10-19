/*
Copyright 2026 CGI France

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
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckPackageSecretRefs checks the secretRef values of the package parameters
// and context. It returns one message per missing Secret or incomplete Secret.
func CheckPackageSecretRefs(ctx context.Context, secretReader client.Reader, namespace string, pckContainer *kubopackage.PckContainer, parameters, kcontext map[string]interface{}) ([]string, ReconcileError) {
	messages, rErr := CheckSecretRefs(ctx, secretReader, namespace, pckContainer.ParamSecretRefDecls, parameters, "parameters")
	if rErr != nil {
		return nil, rErr
	}
	contextMessages, rErr := CheckSecretRefs(ctx, secretReader, namespace, pckContainer.ContextSecretRefDecls, kcontext, "context")
	if rErr != nil {
		return nil, rErr
	}
	return append(messages, contextMessages...), nil
}

// CheckBoundSecretRefs checks the secretRef values of the contracts of the
// connections substituted by connectionRef parameters. Must be called before
// ApplyRefBindings, which removes the resolved values from the input model.
func CheckBoundSecretRefs(ctx context.Context, k8sClient client.Client, secretReader client.Reader, namespace string, inputs []*kubopackage.InputRendered, bindings []RefBinding, result *BuildInputModelResult) ([]string, ReconcileError) {
	bound := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		bound[b.Alias] = true
	}
	messages := make([]string, 0)
	for idx, input := range inputs {
		if !bound[input.Alias] {
			continue
		}
		contract, rErr := getConnectionContract(ctx, k8sClient, result.EffectiveInputConnections[idx], input.Contract)
		if rErr != nil {
			return nil, rErr
		}
		schema, err := contractOpenAPISchema(contract)
		if err != nil {
			return nil, NewReconcileError(fmt.Errorf("contract '%s': %w", input.Contract, err), false, "Secrets")
		}
		decls := kuboschema.CollectSecretRefDecls(schema)
		if len(decls) == 0 {
			continue
		}
		values, _ := result.InputModel[input.Alias].(map[string]interface{})
		inputMessages, rErr := CheckSecretRefs(ctx, secretReader, namespace, decls, values, input.Alias)
		if rErr != nil {
			return nil, rErr
		}
		messages = append(messages, inputMessages...)
	}
	return messages, nil
}

// getConnectionContract follows the lookup of the connection controllers: a
// Connection uses the Contract of its namespace, then the ClusterContract.
func getConnectionContract(ctx context.Context, k8sClient client.Client, ref kv1alpha1.InputConnectionReference, name string) (kv1alpha1.ContractFacade, ReconcileError) {
	if ref.Kind == kv1alpha1.KindConnection {
		contract := &kv1alpha1.Contract{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: name}, contract)
		if err == nil {
			return contract, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, NewReconcileError(fmt.Errorf("could not get contract '%s:%s': %w", ref.Namespace, name, err), false, "Secrets")
		}
	}
	clusterContract := &kv1alpha1.ClusterContract{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, clusterContract)
	if err != nil {
		return nil, NewReconcileError(fmt.Errorf("could not get contract '%s' of connection '%s': %w", name, ref.Name, err), false, "Secrets")
	}
	return clusterContract, nil
}

// CheckSecretRefs checks each secretRef value of the tree in the given namespace.
// prefix names the tree in the messages.
func CheckSecretRefs(ctx context.Context, secretReader client.Reader, namespace string, decls []kuboschema.SecretRefDecl, tree map[string]interface{}, prefix string) ([]string, ReconcileError) {
	messages := make([]string, 0)
	for _, decl := range decls {
		if rErr := checkSecretRefAt(ctx, secretReader, namespace, decl, decl.Path, prefix, tree, &messages); rErr != nil {
			return nil, rErr
		}
	}
	return messages, nil
}

func checkSecretRefAt(ctx context.Context, secretReader client.Reader, namespace string, decl kuboschema.SecretRefDecl, remaining []string, location string, node interface{}, messages *[]string) ReconcileError {
	if len(remaining) == 0 {
		return checkSecret(ctx, secretReader, namespace, decl, location, node, messages)
	}
	seg := remaining[0]
	if seg == "[]" {
		list, ok := node.([]interface{})
		if !ok {
			return nil
		}
		for i, item := range list {
			if rErr := checkSecretRefAt(ctx, secretReader, namespace, decl, remaining[1:], fmt.Sprintf("%s[%d]", location, i), item, messages); rErr != nil {
				return rErr
			}
		}
		return nil
	}
	nodeMap, ok := node.(map[string]interface{})
	if !ok {
		return nil
	}
	child, ok := nodeMap[seg]
	if !ok {
		// Absent: a required value is enforced by the schema validation
		return nil
	}
	return checkSecretRefAt(ctx, secretReader, namespace, decl, remaining[1:], location+"."+seg, child, messages)
}

func checkSecret(ctx context.Context, secretReader client.Reader, namespace string, decl kuboschema.SecretRefDecl, location string, value interface{}, messages *[]string) ReconcileError {
	name, ok := value.(string)
	if !ok {
		return NewReconcileError(fmt.Errorf("'%s' is a %s: it must carry a Secret name (string), got %T", location, kuboschema.TypeSecretRef, value), true, "SecretParameters")
	}
	if name == "" {
		if decl.Required {
			return NewReconcileError(fmt.Errorf("'%s' is required: no Secret name provided", location), true, "SecretParameters")
		}
		return nil
	}
	secret := &corev1.Secret{}
	err := secretReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			*messages = append(*messages, fmt.Sprintf("%s: Secret '%s' not found in namespace '%s'", location, name, namespace))
			return nil
		}
		return NewReconcileError(fmt.Errorf("%s: could not get Secret '%s:%s': %w", location, namespace, name, err), false, "Secrets")
	}
	missing := make([]string, 0)
	for _, key := range decl.Keys {
		if _, ok := secret.Data[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		*messages = append(*messages, fmt.Sprintf("%s: Secret '%s' in namespace '%s' is missing keys: %s", location, name, namespace, strings.Join(missing, ", ")))
	}
	return nil
}
