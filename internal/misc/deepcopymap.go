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

package misc

/*
	From ChatGPT
	Below are the correct and idiomatic approaches, from most explicit (and safe) to most convenient.

	Handles
	- Nested map[string]interface{}
	- []interface{}

	Important: If your map contains:
	- pointers
	- custom structs
	- time.Time
	- channels / funcs (rare)

	You must define how they should be copied.
*/

func DeepCopyMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}

	cp := make(map[string]interface{}, len(m))
	for k, v := range m {
		cp[k] = deepCopy(v)
	}
	return cp
}

func deepCopy(v interface{}) interface{} {
	switch val := v.(type) {

	case map[string]interface{}:
		return DeepCopyMap(val)

	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = deepCopy(item)
		}
		return out

	case map[interface{}]interface{}:
		out := make(map[interface{}]interface{}, len(val))
		for k, v := range val {
			out[k] = deepCopy(v)
		}
		return out

	// immutable / value types → safe to reuse
	case string, int, int64, float64, bool, nil:
		return val

	default:
		// structs, pointers, time.Time, etc.
		// You must decide what to do here
		return val
	}
}
