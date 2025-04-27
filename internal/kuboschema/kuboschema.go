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

package kuboschema

import (
	"fmt"
	"maps"
)

type KuboSchema map[string]interface{}

func Kubo2openAPI(schema KuboSchema, additionalProperties bool) (map[string]interface{}, error) {
	if schema == nil {
		return nil, nil
	}
	//schema, ok := sch.(map[string]interface{})
	//if !ok {
	//	return nil, fmt.Errorf("base object is not a map (type: %T)", sch)
	//}
	if len(schema) == 0 {
		return nil, nil
	}
	_, ok := schema["$schema"]
	if ok {
		// It is already an openAPI schema
		return schema, nil
	}
	//_, ok = schema["properties"]
	//if !ok {
	//	_, ok = schema["type"]
	//	if !ok {
	//		// 'properties' envelope is by default
	//		s2 := make(map[string]interface{})
	//		s2["properties"] = schema
	//		schema = s2
	//	}
	//}
	//_, ok = schema["properties"]
	//if !ok {
	//	return nil, fmt.Errorf("schema does not look like a KuboCD one. (does not contain 'properties')")
	//}
	node, _, err := handleNode("", schema, additionalProperties)
	if err != nil {
		return nil, err
	}
	node["$schema"] = "http://json-schema.org/schema#"
	return node, nil
}

func handleNode(path string, node2 map[string]interface{}, additionalProperties bool) (openAPI map[string]interface{}, required bool, err error) {
	node := maps.Clone(node2)
	// Lookup type
	t, ok := node["type"]
	if !ok {
		_, ok = node["properties"]
		if ok {
			t = "object"
		} else {
			_, ok = node["items"]
			if ok {
				t = "array"
			}
		}
	}
	if t == nil {
		return nil, false, fmt.Errorf("missing type for node '%s'", path)
	}
	typ, ok := t.(string)
	if !ok {
		return nil, false, fmt.Errorf("expected 'type' to be a string (was %T)", t)
	}
	if typ == "" {
		return nil, false, fmt.Errorf("missing type for node '%s'", path)
	}

	allowedProperties, ok := allowedPropertiesByTypes[typ]
	if !ok {
		return nil, false, fmt.Errorf("unknown type '%s' for node '%s'", typ, path)
	}
	for prop := range node {
		_, allowed := allowedProperties[prop]
		if !allowed {
			return nil, false, fmt.Errorf("unknown property '%s' for node '%s'", prop, path)
		}
	}
	switch typ {
	case "string", "integer", "number", "boolean":
		// It is a scalar type. Can use the openAPI directly, except 'required'
		required, err := handleRequired(node)
		if err != nil {
			return nil, false, fmt.Errorf("node '%s': %w", path, err)
		}
		return node, required, nil
	case "object":
		required, err := handleRequired(node)
		if err != nil {
			return nil, false, fmt.Errorf("node '%s': %w", path, err)
		}
		openApiProperties := make(map[string]interface{})
		openApiRequired := make([]string, 0)
		kuboProperties, ok := node["properties"]
		if !ok {
			return nil, false, fmt.Errorf("missing 'properties' attribute for node '%s'", path)
		}
		addProps, ok := node["additionalProperties"]
		if ok {
			_, ok = addProps.(bool)
			if !ok {
				return nil, false, fmt.Errorf("invalud 'additionalProperties' attribute for node '%s' (type is not a boolean)", path)
			}
			additionalProperties = addProps.(bool)
		}

		kuboPropertiesMap, ok := kuboProperties.(map[string]interface{})
		if !ok {
			return nil, false, fmt.Errorf("invalid 'properties' attribute for node '%s' (type is not a map)", path)
		}
		for k, v := range kuboPropertiesMap {
			vMap, ok := v.(map[string]interface{})
			if !ok {
				return nil, false, fmt.Errorf("invalid 'properties.%s' value for node '%s' (type is not a map)", path, v)
			}
			openApiProperty, required, err := handleNode(path+"."+k, vMap, additionalProperties)
			if err != nil {
				return nil, false, err
			}
			if required {
				openApiRequired = append(openApiRequired, k)
			}
			openApiProperties[k] = openApiProperty
		}
		object := node // make(map[string]interface{})
		object["type"] = "object"
		object["additionalProperties"] = additionalProperties
		object["required"] = openApiRequired
		object["properties"] = openApiProperties
		return object, required, nil
	case "array":
		required, err := handleRequired(node)
		if err != nil {
			return nil, false, fmt.Errorf("node '%s': %w", path, err)
		}
		items, ok := node["items"]
		if !ok {
			return nil, false, fmt.Errorf("node '%s'.type is 'array', but there is no 'items' property", path)
		}
		itemsMap, ok := items.(map[string]interface{})
		if !ok {
			return nil, false, fmt.Errorf("node '%s'.type is 'array', but 'items' property is not a map", path)
		}
		n, _, err := handleNode(path+".[]", itemsMap, additionalProperties)
		if err != nil {
			return nil, false, err
		}
		object := node //make(map[string]interface{})
		object["type"] = "array"
		object["items"] = n
		return object, required, nil
	default:
		return nil, false, fmt.Errorf("%s: unknown type '%s'", path, typ)
	}
}

func handleRequired(node map[string]interface{}) (bool, error) {
	req, ok := node["required"]
	if !ok {
		return false, nil
	}
	b, ok := req.(bool)
	if !ok {
		return false, fmt.Errorf("property 'required' is not a boolean")
	}
	delete(node, "required")
	return b, nil
}

var allowedPropertiesByTypes = map[string]map[string]bool{
	"string": {
		"type":        true,
		"title":       true,
		"description": true,
		"enum":        true,
		"required":    true,
		"default":     true,
		"minLength":   true,
		"maxLength":   true,
		"pattern":     true,
	},
	"integer": {
		"type":             true,
		"title":            true,
		"description":      true,
		"enum":             true,
		"required":         true,
		"default":          true,
		"minimum":          true,
		"maximum":          true,
		"exclusiveMinimum": true,
		"exclusiveMaximum": true,
		"multipleOf":       true,
	},
	"number": {
		"type":             true,
		"title":            true,
		"description":      true,
		"enum":             true,
		"required":         true,
		"default":          true,
		"minimum":          true,
		"maximum":          true,
		"exclusiveMinimum": true,
		"exclusiveMaximum": true,
		"multipleOf":       true,
	},
	"boolean": {
		"type":        true,
		"title":       true,
		"description": true,
		"enum":        true,
		"required":    true,
		"default":     true,
	},
	"object": {
		"type":                 true,
		"title":                true,
		"description":          true,
		"required":             true,
		"properties":           true,
		"additionalProperties": true,
		"patternProperties":    true,
		"minProperties":        true,
		"maxProperties":        true,
	},
	"array": {
		"type":        true,
		"title":       true,
		"description": true,
		"required":    true,
		"items":       true,
		"prefixItems": true,
		"maxItems":    true,
		"minItems":    true,
		"uniqueItems": true,
	},
}
