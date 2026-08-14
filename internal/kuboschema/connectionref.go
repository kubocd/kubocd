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

package kuboschema

import (
	"fmt"
	"strings"
)

// The connection-aware schema type. A connectionRef types a parameter (or a
// context variable) carrying the NAME of a connection: it is de-sugared to a
// plain string, and the controller generates the matching input, then
// substitutes the resolved values in place.
const TypeConnectionRef = "connectionRef"

// Marker left in the de-sugared (openAPI) schema. gojsonschema ignores
// unknown keywords, so it is transparent for validation.
const MarkerConnectionRef = "x-kubocd-connection-ref"

var connectionRefAllowedProperties = map[string]bool{
	"type":        true,
	"title":       true,
	"description": true,
	"required":    true,
	"default":     true,
	"interface":   true,
	"kind":        true,
}

// desugarConnectionRef turns a connectionRef node into a string node carrying
// the marker. The default, when present, must be a template string (rendered
// against the Context): a literal default would hardcode a wiring target in
// the package, which is forbidden.
func desugarConnectionRef(path string, node map[string]interface{}) (map[string]interface{}, bool, error) {
	iface, err := connectionInterface(path, node)
	if err != nil {
		return nil, false, err
	}
	if def, ok := node["default"]; ok {
		defStr, isStr := def.(string)
		if !isStr || !strings.Contains(defStr, "{{") {
			return nil, false, fmt.Errorf("node '%s': the 'default' of a %s must be a template string (rendered against the Context), literal targets are not allowed", path, TypeConnectionRef)
		}
	}
	marker := map[string]interface{}{"interface": iface}
	if err := connectionKind(path, node, marker); err != nil {
		return nil, false, err
	}
	required, err := handleRequired(node)
	if err != nil {
		return nil, false, fmt.Errorf("node '%s': %w", path, err)
	}
	node["type"] = "string"
	delete(node, "interface")
	delete(node, "kind")
	node[MarkerConnectionRef] = marker
	return node, required, nil
}

// connectionKind reads the optional 'kind' restriction and stores it in the
// marker. Absent means "look both kinds up", which is only ambiguous when the
// same name (or interface) is carried by a Connection AND a ClusterConnection.
func connectionKind(path string, node map[string]interface{}, marker map[string]interface{}) error {
	kind, ok := node["kind"]
	if !ok {
		return nil
	}
	kindStr, isStr := kind.(string)
	if !isStr || (kindStr != "Connection" && kindStr != "ClusterConnection") {
		return fmt.Errorf("node '%s': 'kind' must be 'Connection' or 'ClusterConnection'", path)
	}
	marker["kind"] = kindStr
	return nil
}

func connectionInterface(path string, node map[string]interface{}) (string, error) {
	iface, ok := node["interface"]
	if !ok {
		return "", fmt.Errorf("node '%s': 'interface' is required", path)
	}
	ifaceStr, ok := iface.(string)
	if !ok || ifaceStr == "" {
		return "", fmt.Errorf("node '%s': 'interface' must be a non-empty string", path)
	}
	return ifaceStr, nil
}

// ConnectionDecl is one connectionRef declaration found in a de-sugared
// schema.
type ConnectionDecl struct {
	// Path segments from the schema root. An array traversal is the "[]"
	// segment, replaced by the actual index at generation time.
	Path []string
	// The connection interface
	Interface string
	// The parameter (or context variable) is required
	Required bool
	// The default template ("" if none)
	Default string
	// Restrict the lookup to one kind ("" = both)
	KindFilter string
}

// CollectConnectionDecls walks a de-sugared schema and returns the connection
// declarations. Position rule: a connectionRef in schema.context cannot have a
// default (the name always comes from the Context).
func CollectConnectionDecls(schema map[string]interface{}, isContext bool) ([]ConnectionDecl, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	decls := make([]ConnectionDecl, 0)
	err := collectConnectionDecls(schema, nil, false, false, isContext, &decls)
	if err != nil {
		return nil, err
	}
	return decls, nil
}

func collectConnectionDecls(node map[string]interface{}, path []string, required bool, inArray bool, isContext bool, decls *[]ConnectionDecl) error {
	pathStr := strings.Join(path, ".")
	if marker, ok := node[MarkerConnectionRef]; ok {
		markerMap, _ := marker.(map[string]interface{})
		iface, _ := markerMap["interface"].(string)
		kind, _ := markerMap["kind"].(string)
		def, _ := node["default"].(string)
		if isContext && def != "" {
			return fmt.Errorf("node '%s': a %s in schema.context cannot have a default, the name always comes from the Context", pathStr, TypeConnectionRef)
		}
		if inArray && def != "" {
			// The defaulter never descends into array items: such a default
			// would be silently dead. Reject loudly.
			return fmt.Errorf("node '%s': a %s inside array items cannot have a default", pathStr, TypeConnectionRef)
		}
		if err := checkRefPathSegments(path); err != nil {
			return err
		}
		*decls = append(*decls, ConnectionDecl{
			Path:       append([]string{}, path...),
			Interface:  iface,
			Required:   required,
			Default:    def,
			KindFilter: kind,
		})
		return nil
	}
	if properties, ok := node["properties"].(map[string]interface{}); ok {
		requiredSet := make(map[string]bool)
		if reqList, ok := node["required"].([]string); ok {
			for _, r := range reqList {
				requiredSet[r] = true
			}
		} else if reqList, ok := node["required"].([]interface{}); ok {
			for _, r := range reqList {
				if s, isStr := r.(string); isStr {
					requiredSet[s] = true
				}
			}
		}
		for k, v := range properties {
			child, ok := v.(map[string]interface{})
			if !ok {
				continue
			}
			if err := collectConnectionDecls(child, append(path, k), requiredSet[k], inArray, isContext, decls); err != nil {
				return err
			}
		}
	}
	if items, ok := node["items"].(map[string]interface{}); ok {
		if err := collectConnectionDecls(items, append(path, "[]"), false, true, isContext, decls); err != nil {
			return err
		}
	}
	return nil
}

// checkRefPathSegments rejects all-digit property names on a connection
// declaration path: the substitution machinery distinguishes map keys from
// array indices by their digit-only shape.
func checkRefPathSegments(path []string) error {
	for _, seg := range path {
		if seg == "[]" {
			continue
		}
		allDigits := len(seg) > 0
		for _, c := range seg {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return fmt.Errorf("property '%s': an all-digit property name is not supported on a connection declaration path", strings.Join(path, "."))
		}
	}
	return nil
}
