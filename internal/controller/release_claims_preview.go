/*
Copyright 2026 Kubotal

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
	"encoding/json"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"kubocd/internal/misc"
	"maps"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClaimPreview says what a claim parameter received when rendered without a cluster binding it.
type ClaimPreview struct {
	Parameter   string `json:"parameter"`
	Claim       string `json:"claim"`
	Contract    string `json:"contract"`
	Connection  string `json:"connection,omitempty"`
	Provisioner string `json:"provisioner,omitempty"`
	// Properties of the contract the provisioner sets for each claim, given a placeholder
	Placeholders []string `json:"placeholders,omitempty"`
}

// ClaimsPreviewResult gives each claim parameter the values it would get once bound.
type ClaimsPreviewResult struct {
	// Inputs of the claims naming a connection, resolved like a connectionRef
	Inputs   []*kubopackage.InputRendered
	Bindings []RefBinding
	// Values of the claims naming a provisioner, by alias, to add to the input model before the bindings apply
	Values   map[string]interface{}
	Previews []ClaimPreview
}

// PreviewClaims resolves the claim parameters without creating any Claim. A claim naming a connection gets
// its values. A claim naming a provisioner gets the values the provisioner announces, the default values of
// the contract, its requirements of the same name as a property, and a placeholder for each other property.
// The requirements are checked at deploy time only.
func PreviewClaims(ctx context.Context, reader client.Reader, release *kv1alpha1.Release, decls []kuboschema.ClaimDecl,
	parameters map[string]interface{}, inputs []*kubopackage.InputRendered) (*ClaimsPreviewResult, error) {
	params, err := claimParameters(release.Name, decls, parameters, inputs)
	if err != nil {
		return nil, err
	}
	result := &ClaimsPreviewResult{Values: make(map[string]interface{})}
	for _, param := range params {
		preview := ClaimPreview{Parameter: param.Alias, Claim: param.Name, Contract: param.Contract}
		result.Bindings = append(result.Bindings, RefBinding{Alias: param.Alias, Path: param.Path})
		if param.Target.Connection != "" {
			input := &kubopackage.InputRendered{Contract: param.Contract, Kind: kv1alpha1.KindConnection, Alias: param.Alias}
			input.ConnectionRef.Name = param.Target.Connection
			input.ConnectionRef.Namespace = release.Namespace
			result.Inputs = append(result.Inputs, input)
			preview.Connection = param.Target.Connection
		} else {
			values, placeholders, err := provisionerPreview(ctx, reader, release.Namespace, param)
			if err != nil {
				return nil, fmt.Errorf("claim '%s': %w", param.Name, err)
			}
			result.Values[param.Alias] = values
			preview.Provisioner = param.Target.Provisioner
			preview.Placeholders = placeholders
		}
		result.Previews = append(result.Previews, preview)
	}
	return result, nil
}

func provisionerPreview(ctx context.Context, reader client.Reader, namespace string, param claimParameter) (map[string]interface{}, []string, error) {
	announced, err := announcedValues(ctx, reader, namespace, param.Target.Provisioner, param.Contract)
	if err != nil {
		return nil, nil, err
	}
	contract, err := findContractNamed(ctx, reader, param.Contract, namespace)
	if err != nil {
		return nil, nil, err
	}
	if contract == nil {
		return nil, nil, fmt.Errorf("contract '%s' not found", param.Contract)
	}
	defaults, schema, _, err := resolveContract(contract)
	if err != nil {
		return nil, nil, fmt.Errorf("contract '%s' is in error: %w", param.Contract, err)
	}
	values := misc.MergeMaps(defaults, announced)
	properties, _ := schema["properties"].(map[string]interface{})
	placeholders := make([]string, 0)
	for _, name := range slices.Sorted(maps.Keys(properties)) {
		// The defaulter gives an empty list to every array: that is no value.
		if value, set := values[name]; set && !isEmptyList(value) {
			continue
		}
		if requirement, ok := param.Requirements[name]; ok {
			values[name] = requirement
			continue
		}
		property, _ := properties[name].(map[string]interface{})
		values[name] = placeholder(property, param.Target.Provisioner)
		placeholders = append(placeholders, name)
	}
	return values, placeholders, nil
}

// announcedValues reads the values a provisioner of the contract serving the namespace announces.
func announcedValues(ctx context.Context, reader client.Reader, namespace, provisioner, contract string) (map[string]interface{}, error) {
	releases := &kv1alpha1.ReleaseList{}
	if err := reader.List(ctx, releases); err != nil {
		return nil, fmt.Errorf("could not list the releases: %s", apiError(err))
	}
	for _, r := range releases.Items {
		for _, p := range r.Status.Provisioners {
			if p.Name != provisioner || p.Contract != contract || !slices.Contains(p.Namespaces, namespace) {
				continue
			}
			values := make(map[string]interface{})
			if p.Values != nil {
				if err := json.Unmarshal(p.Values.Raw, &values); err != nil {
					return nil, fmt.Errorf("the values of provisioner '%s' are not a map", provisioner)
				}
			}
			return values, nil
		}
	}
	return nil, fmt.Errorf("no provisioner '%s' of contract '%s' serves namespace '%s'", provisioner, contract, namespace)
}

// placeholder is a value of the type of the property, saying that the provisioner sets it.
func placeholder(property map[string]interface{}, provisioner string) interface{} {
	text := fmt.Sprintf("<set by provisioner %s>", provisioner)
	switch property["type"] {
	case "array":
		return []interface{}{text}
	case "boolean":
		return false
	case "integer", "number":
		return 0
	case "object":
		return map[string]interface{}{}
	}
	return text
}

func isEmptyList(value interface{}) bool {
	list, ok := value.([]interface{})
	return ok && len(list) == 0
}
