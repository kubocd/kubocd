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

//nolint:goconst // JSON schema keywords read better inline, as in the rest of the package.
package kuboschema

import (
	"fmt"
	"slices"
	"strings"
)

// The claim schema type. A claim parameter carries the target of a Claim: a
// provisioner or an existing connection. The controller creates the Claim,
// then substitutes the values of the bound connection in place.
const TypeClaim = "claim"

// Marker left in the de-sugared (openAPI) schema, with the contract and the requirements.
const MarkerClaim = "x-kubocd-claim"

var claimAllowedProperties = map[string]bool{
	"type":         true,
	"title":        true,
	"description":  true,
	"required":     true,
	"contract":     true,
	"requirements": true,
}

// shapeClaim gives a node carrying the marker the object a claim parameter takes: exactly one of
// provisioner and connection, and the requirements the Release adds to those of the package.
func shapeClaim(node map[string]interface{}) {
	node["type"] = "object"
	node["additionalProperties"] = false
	node["properties"] = map[string]interface{}{
		"provisioner":  map[string]interface{}{"type": "string", "minLength": 1},
		"connection":   map[string]interface{}{"type": "string", "minLength": 1},
		"requirements": map[string]interface{}{"type": "object"},
	}
	node["oneOf"] = []interface{}{
		map[string]interface{}{"required": []interface{}{"provisioner"}},
		map[string]interface{}{"required": []interface{}{"connection"}},
	}
	delete(node, "minProperties")
	delete(node, "maxProperties")
}

// shapeClaims shapes the claim parameters of an openAPI schema, which carry the marker only.
func shapeClaims(node map[string]interface{}) {
	if _, ok := node[MarkerClaim].(map[string]interface{}); ok {
		shapeClaim(node)
		return
	}
	if properties, ok := node["properties"].(map[string]interface{}); ok {
		for _, v := range properties {
			if child, ok := v.(map[string]interface{}); ok {
				shapeClaims(child)
			}
		}
	}
	if items, ok := node["items"].(map[string]interface{}); ok {
		shapeClaims(items)
	}
}

// desugarClaim turns a claim node into the claim object, carrying the marker.
func desugarClaim(path string, node map[string]interface{}) (map[string]interface{}, bool, error) {
	contract, err := contractOf(path, node)
	if err != nil {
		return nil, false, err
	}
	marker := map[string]interface{}{"contract": contract}
	if requirements, ok := node["requirements"]; ok {
		if _, isMap := requirements.(map[string]interface{}); !isMap {
			return nil, false, fmt.Errorf("node '%s': 'requirements' must be a map", path)
		}
		marker["requirements"] = requirements
	}
	required, err := handleRequired(node)
	if err != nil {
		return nil, false, fmt.Errorf("node '%s': %w", path, err)
	}
	object := map[string]interface{}{MarkerClaim: marker}
	shapeClaim(object)
	for _, key := range []string{"title", "description"} {
		if value, ok := node[key]; ok {
			object[key] = value
		}
	}
	return object, required, nil
}

// ClaimDecl is one claim declaration found in a de-sugared schema.
type ClaimDecl struct {
	// Path segments from the schema root
	Path         []string
	Contract     string
	Required     bool
	Requirements map[string]interface{}
}

// CollectClaimDecls walks a de-sugared schema and returns the claim
// declarations, sorted by path. A claim is refused in schema.context, as each
// Release makes its own claims, and inside array items.
func CollectClaimDecls(schema map[string]interface{}, isContext bool) ([]ClaimDecl, error) {
	decls := make([]ClaimDecl, 0)
	if err := collectClaimDecls(schema, nil, false, false, isContext, &decls); err != nil {
		return nil, err
	}
	slices.SortFunc(decls, func(a, b ClaimDecl) int {
		return slices.Compare(a.Path, b.Path)
	})
	return decls, nil
}

func collectClaimDecls(node map[string]interface{}, path []string, required bool, inArray bool, isContext bool, decls *[]ClaimDecl) error {
	if marker, ok := node[MarkerClaim].(map[string]interface{}); ok {
		pathStr := strings.Join(path, ".")
		if isContext {
			return fmt.Errorf("node '%s': a %s is not allowed in schema.context", pathStr, TypeClaim)
		}
		if inArray {
			return fmt.Errorf("node '%s': a %s is not allowed inside array items", pathStr, TypeClaim)
		}
		if err := checkRefPathSegments(path); err != nil {
			return err
		}
		contract, _ := marker["contract"].(string)
		if contract == "" {
			return fmt.Errorf("node '%s': the %s marker names no contract", pathStr, MarkerClaim)
		}
		requirements, _ := marker["requirements"].(map[string]interface{})
		*decls = append(*decls, ClaimDecl{Path: append([]string{}, path...), Contract: contract, Required: required,
			Requirements: requirements})
		return nil
	}
	if properties, ok := node["properties"].(map[string]interface{}); ok {
		requiredSet := requiredProperties(node)
		for k, v := range properties {
			child, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			if err := collectClaimDecls(child, append(path, k), requiredSet[k], inArray, isContext, decls); err != nil {
				return err
			}
		}
	}
	if items, ok := node["items"].(map[string]interface{}); ok {
		if err := collectClaimDecls(items, append(path, "[]"), false, true, isContext, decls); err != nil {
			return err
		}
	}
	return nil
}
