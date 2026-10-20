/*
Copyright 2026 Alexandre MABROUK

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

package kubopackage

import (
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"slices"
	"strings"
)

// renderNamedInput handles the inputs whose connection name is given and may be empty: the ones read from the
// parameters, and the ones whose name is rendered empty, which bind nothing when optional.
func renderNamedInput(ir *InputRendered, raw map[string]interface{}, model map[string]interface{}, defaultNamespace string) ([]*InputRendered, bool, error) {
	if ir.FromParameter == "" && !explicitlyEmpty(raw, "connectionRef") && !explicitlyEmpty(raw, "releaseRef") {
		return nil, false, nil
	}
	if ir.Contract == "" {
		return nil, true, fmt.Errorf("contract is required")
	}
	if ir.Alias == "" {
		ir.Alias = ir.Contract
	}
	if ir.FromParameter == "" {
		if !ir.Optional {
			return nil, true, fmt.Errorf("the connection name is empty")
		}
		ir.unbound = true
		return []*InputRendered{ir}, true, nil
	}
	if ir.ConnectionRef.Name != "" || ir.ConnectionRef.Namespace != "" || ir.ReleaseRef.Name != "" ||
		ir.ReleaseRef.Namespace != "" || ir.AllowMultiple {
		return nil, true, fmt.Errorf("fromParameter excludes connectionRef, releaseRef and allowMultiple")
	}
	parameters, _ := model["Parameters"].(map[string]interface{})
	inputs, err := expandFromParameter(ir, parameters, defaultNamespace)
	return inputs, true, err
}

// explicitlyEmpty reports an input whose ref (connectionRef or releaseRef) has a name rendered empty.
func explicitlyEmpty(raw map[string]interface{}, ref string) bool {
	m, _ := raw[ref].(map[string]interface{})
	name, ok := m["name"]
	return ok && (name == nil || name == "")
}

// expandFromParameter turns an input whose connection names come from the parameters into one input per
// name, each a connectionRef. With [] in the path, the inputs share the list of .InputLists.<alias>, which
// follows the arrays of the parameters, and the first returned input only declares that list.
func expandFromParameter(ir *InputRendered, parameters map[string]interface{}, defaultNamespace string) ([]*InputRendered, error) {
	steps, err := parameterSteps(ir.FromParameter)
	if err != nil {
		return nil, err
	}
	out := make([]*InputRendered, 0)
	var fail error
	emit := func(item []int, name string) {
		in := &InputRendered{Contract: ir.Contract, Kind: ir.Kind, Alias: ir.Alias, Optional: ir.Optional,
			FromParameter: ir.FromParameter, item: item}
		in.ConnectionRef.Name = name
		if in.Kind != kv1alpha1.KindClusterConnection {
			in.ConnectionRef.Namespace = defaultNamespace
		}
		if name == "" {
			if !ir.Optional && fail == nil {
				fail = fmt.Errorf("parameter '%s' names no connection", ir.FromParameter)
			}
			in.unbound = true
		}
		out = append(out, in)
	}
	list, err := walkParameter(parameters, steps, ir.FromParameter, nil, emit)
	if err != nil {
		return nil, err
	}
	if fail != nil {
		return nil, fail
	}
	if !slices.Contains(steps, "[]") {
		return out, nil
	}
	head := &InputRendered{Contract: ir.Contract, Kind: ir.Kind, Alias: ir.Alias, Optional: ir.Optional,
		FromParameter: ir.FromParameter, unbound: true}
	out = append([]*InputRendered{head}, out...)
	for _, in := range out {
		in.list = list.([]interface{})
	}
	return out, nil
}

// parameterSteps splits 'a.b[].c' into [a b [] c].
func parameterSteps(path string) ([]string, error) {
	steps := make([]string, 0)
	for _, token := range strings.Split(path, ".") {
		name := strings.TrimRight(token, "[]")
		suffix := token[len(name):]
		if name == "" || strings.ContainsAny(name, "[]") || len(suffix)%2 != 0 || strings.ReplaceAll(suffix, "[]", "") != "" {
			return nil, fmt.Errorf("fromParameter '%s': expected properties joined by dots, [] for each item", path)
		}
		steps = append(steps, name)
		for range len(suffix) / 2 {
			steps = append(steps, "[]")
		}
	}
	return steps, nil
}

// walkParameter calls emit for each name found along steps, with its position in the arrays, and returns
// the lists that mirror the arrays (nil without []), each slot empty until bound.
func walkParameter(value interface{}, steps []string, path string, item []int, emit func([]int, string)) (interface{}, error) {
	if len(steps) == 0 {
		switch v := value.(type) {
		case nil:
			emit(item, "")
		case string:
			emit(item, v)
		default:
			return nil, fmt.Errorf("parameter '%s': a connection name is a string", path)
		}
		return nil, nil
	}
	if steps[0] == "[]" {
		var items []interface{}
		if value != nil {
			var ok bool
			if items, ok = value.([]interface{}); !ok {
				return nil, fmt.Errorf("parameter '%s': expected a list", path)
			}
		}
		list := make([]interface{}, len(items))
		for i, v := range items {
			sub, err := walkParameter(v, steps[1:], path, append(slices.Clone(item), i), emit)
			if err != nil {
				return nil, err
			}
			list[i] = sub
		}
		return list, nil
	}
	var next interface{}
	if value != nil {
		m, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("parameter '%s': expected an object at '%s'", path, steps[0])
		}
		next = m[steps[0]]
	}
	return walkParameter(next, steps[1:], path, item, emit)
}
