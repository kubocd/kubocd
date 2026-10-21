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

package tmpl

import (
	"fmt"
	"slices"
)

// plainCopy returns a deep copy of model, which must hold plain data only:
// maps with string keys, lists and scalars. A template can call the exported
// methods of any other value it reaches. It can also modify the maps and
// lists it receives (set, sortAlpha), hence a copy for each execution.
func plainCopy(model map[string]interface{}) (map[string]interface{}, error) {
	var violations []string
	data := make(map[string]interface{}, len(model))
	for key, value := range model {
		data[key] = copyValue(key, value, &violations)
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		return nil, fmt.Errorf("the template data is not plain data: %s", violations[0])
	}
	return data, nil
}

func copyValue(path string, value interface{}, violations *[]string) interface{} {
	switch v := value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return v
	case map[string]interface{}:
		if v == nil {
			return v
		}
		out := make(map[string]interface{}, len(v))
		for key, item := range v {
			out[key] = copyValue(path+"."+key, item, violations)
		}
		return out
	case []interface{}:
		if v == nil {
			return v
		}
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = copyValue(fmt.Sprintf("%s[%d]", path, i), item, violations)
		}
		return out
	case []string:
		return slices.Clone(v)
	case []map[string]interface{}:
		if v == nil {
			return v
		}
		out := make([]map[string]interface{}, len(v))
		for i, item := range v {
			out[i], _ = copyValue(fmt.Sprintf("%s[%d]", path, i), item, violations).(map[string]interface{})
		}
		return out
	default:
		*violations = append(*violations, fmt.Sprintf("%s is a %T", path, value))
		return nil
	}
}
