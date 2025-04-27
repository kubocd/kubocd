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
	"kubocd/internal/misc"
)

func Defaulter(schema KuboSchema) (map[string]interface{}, error) {
	def, err := BaseDefaulter(schema)
	if err != nil {
		return nil, err
	}
	_, ok := def.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("resulting default value is not a map")
	}
	return def.(map[string]interface{}), nil
}

func BaseDefaulter(schema KuboSchema) (interface{}, error) {
	if misc.IsZero(schema) {
		return map[string]interface{}{}, nil
	}
	//schema, ok := sch.(map[string]interface{})
	//if !ok {
	//	return nil, fmt.Errorf("schema is not a map")
	//}
	_, ok := schema["$schema"]
	if !ok {
		return nil, fmt.Errorf("$schema is not defined. Seems it is not an openAPI schema")
	}
	result, err := defaulterHandleNode("", schema)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return map[string]interface{}{}, nil
	}
	return result, nil
}

func defaulterHandleNode(path string, node map[string]interface{}) (interface{}, error) {
	properties, ok := node["properties"]
	if ok {
		propertiesMap, ok := properties.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("node '%s': properties is not a map", path)
		}
		result := make(map[string]interface{})
		for k, v := range propertiesMap {
			property, ok := v.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("node '%s': properties is not a map", path)
			}
			if path != "" {
				path = path + "." + k
			} else {
				path = k
			}
			node2, err := defaulterHandleNode(path, property)
			if err != nil {
				return nil, err
			}
			if node2 != nil {
				result[k] = node2
			}
		}
		if len(result) == 0 {
			return nil, nil
		}
		return result, nil
	}
	t, ok := node["type"]
	if ok {
		t, ok = t.(string)
		if ok && t == "array" {
			// It is an array
			return make([]interface{}, 0), nil
		}
	}
	// It is a simple type
	defaultVal, ok := node["default"]
	if ok {
		return defaultVal, nil
	}
	return nil, nil
}
