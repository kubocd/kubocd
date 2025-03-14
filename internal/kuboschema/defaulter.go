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
