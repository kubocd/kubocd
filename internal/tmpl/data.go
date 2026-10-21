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

// checkPlainData returns an error unless model holds plain data only: maps
// with string keys, lists and scalars. A template can call the exported
// methods of any other value it reaches.
func checkPlainData(model map[string]interface{}) error {
	var violations []string
	for key, value := range model {
		checkValue(key, value, &violations)
	}
	if len(violations) == 0 {
		return nil
	}
	slices.Sort(violations)
	return fmt.Errorf("the template data is not plain data: %s", violations[0])
}

func checkValue(path string, value interface{}, violations *[]string) {
	switch v := value.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, []string:
	case map[string]interface{}:
		for key, item := range v {
			checkValue(path+"."+key, item, violations)
		}
	case []interface{}:
		for i, item := range v {
			checkValue(fmt.Sprintf("%s[%d]", path, i), item, violations)
		}
	case []map[string]interface{}:
		for i, item := range v {
			checkValue(fmt.Sprintf("%s[%d]", path, i), item, violations)
		}
	default:
		*violations = append(*violations, fmt.Sprintf("%s is a %T", path, value))
	}
}
