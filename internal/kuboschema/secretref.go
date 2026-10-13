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

package kuboschema

import (
	"fmt"
)

// The secret-aware schema type. A secretRef types a value carrying the NAME
// of a Secret, which must carry the listed keys. It is de-sugared to a plain
// string, and the controller checks the Secret in the Release namespace.
const TypeSecretRef = "secretRef"

// Marker left in the de-sugared (openAPI) schema. gojsonschema ignores
// unknown keywords, so it is transparent for validation.
const MarkerSecretRef = "x-kubocd-secret-ref"

var secretRefAllowedProperties = map[string]bool{
	keyType:        true,
	keyTitle:       true,
	keyDescription: true,
	keyRequired:    true,
	keyDefault:     true,
	keyKeys:        true,
}

const (
	keyType        = "type"
	keyTitle       = "title"
	keyDescription = "description"
	keyRequired    = "required"
	keyDefault     = "default"
	keyKeys        = "keys"
	typeString     = "string"
)

func desugarSecretRef(path string, node map[string]interface{}) (map[string]interface{}, bool, error) {
	keys, err := secretKeysOf(path, node)
	if err != nil {
		return nil, false, err
	}
	if def, ok := node[keyDefault]; ok {
		if _, isStr := def.(string); !isStr {
			return nil, false, fmt.Errorf("node '%s': the 'default' of a %s must be a string", path, TypeSecretRef)
		}
	}
	required, err := handleRequired(node)
	if err != nil {
		return nil, false, fmt.Errorf("node '%s': %w", path, err)
	}
	markerKeys := make([]interface{}, len(keys))
	for i, k := range keys {
		markerKeys[i] = k
	}
	node[keyType] = typeString
	delete(node, keyKeys)
	node[MarkerSecretRef] = map[string]interface{}{keyKeys: markerKeys}
	return node, required, nil
}

func secretKeysOf(path string, node map[string]interface{}) ([]string, error) {
	raw, ok := node[keyKeys]
	if !ok {
		return nil, fmt.Errorf("node '%s': 'keys' is required", path)
	}
	keys, ok := toStringList(raw)
	if !ok || len(keys) == 0 {
		return nil, fmt.Errorf("node '%s': 'keys' must be a non-empty list of strings", path)
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if k == "" {
			return nil, fmt.Errorf("node '%s': 'keys' cannot contain an empty key", path)
		}
		if seen[k] {
			return nil, fmt.Errorf("node '%s': duplicate key '%s' in 'keys'", path, k)
		}
		seen[k] = true
	}
	return keys, nil
}

func toStringList(raw interface{}) ([]string, bool) {
	switch l := raw.(type) {
	case []string:
		return l, true
	case []interface{}:
		out := make([]string, len(l))
		for i, item := range l {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

// SecretRefDecl is one secretRef declaration found in a de-sugared schema.
type SecretRefDecl struct {
	// Path segments from the schema root. An array traversal is the "[]"
	// segment.
	Path []string
	// The keys the Secret must carry
	Keys []string
	// The value is required
	Required bool
}

// CollectSecretRefDecls walks a de-sugared schema and returns the secretRef
// declarations.
func CollectSecretRefDecls(schema map[string]interface{}) []SecretRefDecl {
	decls := make([]SecretRefDecl, 0)
	collectSecretRefDecls(schema, nil, false, &decls)
	return decls
}

func collectSecretRefDecls(node map[string]interface{}, path []string, required bool, decls *[]SecretRefDecl) {
	if marker, ok := node[MarkerSecretRef].(map[string]interface{}); ok {
		keys, _ := toStringList(marker[keyKeys])
		*decls = append(*decls, SecretRefDecl{
			Path:     append([]string{}, path...),
			Keys:     keys,
			Required: required,
		})
		return
	}
	if properties, ok := node["properties"].(map[string]interface{}); ok {
		requiredList, _ := toStringList(node[keyRequired])
		requiredSet := make(map[string]bool, len(requiredList))
		for _, r := range requiredList {
			requiredSet[r] = true
		}
		for k, v := range properties {
			if child, ok := v.(map[string]interface{}); ok {
				collectSecretRefDecls(child, append(path, k), requiredSet[k], decls)
			}
		}
	}
	if items, ok := node["items"].(map[string]interface{}); ok {
		collectSecretRefDecls(items, append(path, "[]"), false, decls)
	}
}
