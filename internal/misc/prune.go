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

package misc

// Prune recursively remove all null item of the given map
func Prune(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			pruned := Prune(nested)
			if len(pruned) > 0 {
				result[k] = pruned
			}
			continue
		}
		if slice, ok := v.([]interface{}); ok {
			result[k] = pruneSlice(slice)
			continue
		}
		result[k] = v
	}
	return result
}

func pruneSlice(s []interface{}) []interface{} {
	result := make([]interface{}, 0, len(s))
	for _, v := range s {
		if v == nil {
			continue
		}
		if nested, ok := v.(map[string]interface{}); ok {
			pruned := Prune(nested)
			if len(pruned) > 0 {
				result = append(result, pruned)
			}
			continue
		}
		if slice, ok := v.([]interface{}); ok {
			result = append(result, pruneSlice(slice))
			continue
		}
		result = append(result, v)
	}
	return result
}
