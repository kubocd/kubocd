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

package controller

import (
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"kubocd/internal/tmpl"
	"strconv"
	"strings"
)

// The connectionRef / connectionSelector parameter types generate their input
// entries: alias = the declaration path (parameters.datasources[1].trino,
// context.platform.connections.oidc), unique by construction. After
// BuildInputModel, the resolved values replace the connection name IN PLACE
// in the Parameters / Context maps: templates read the declaration point
// (.Parameters.db.host), and .Inputs / .InputLists stay reserved for the
// hand-written stanza.

// RefBinding links a generated input (by alias) to the location, in the
// Parameters or Context tree, where the resolved values must be substituted.
type RefBinding struct {
	Alias string
	// Path in the tree: map keys and array indices (as decimal strings)
	Path []string
	// The tree: true for Context, false for Parameters
	InContext bool
	// true for a selector: the resolved LIST is substituted
	List bool
}

// GenerateRefInputs walks the connection declarations of the package schema
// and the ACTUAL parameter / context values, and produces the generated
// inputs plus their bindings. Stanza aliases are checked for collisions.
func GenerateRefInputs(paramDecls, contextDecls []kuboschema.ConnectionDecl, parameters, context map[string]interface{}, releaseNamespace string, stanzaInputs []kubopackage.InputRendered) ([]kubopackage.InputRendered, []RefBinding, error) {
	generated := make([]kubopackage.InputRendered, 0)
	bindings := make([]RefBinding, 0)
	err := generateFromDecls(paramDecls, parameters, false, releaseNamespace, &generated, &bindings)
	if err != nil {
		return nil, nil, err
	}
	err = generateFromDecls(contextDecls, context, true, releaseNamespace, &generated, &bindings)
	if err != nil {
		return nil, nil, err
	}
	// A stanza alias colliding with a generated one would fight for the same
	// entry of the input model
	stanzaAliases := make(map[string]bool, len(stanzaInputs))
	for _, si := range stanzaInputs {
		stanzaAliases[si.Alias] = true
	}
	for _, gi := range generated {
		if stanzaAliases[gi.Alias] {
			return nil, nil, fmt.Errorf("the inputs stanza alias '%s' collides with a generated connection parameter", gi.Alias)
		}
	}
	return generated, bindings, nil
}

func generateFromDecls(decls []kuboschema.ConnectionDecl, tree map[string]interface{}, inContext bool, releaseNamespace string, generated *[]kubopackage.InputRendered, bindings *[]RefBinding) error {
	for _, decl := range decls {
		err := expandDecl(decl, decl.Path, nil, tree, inContext, releaseNamespace, generated, bindings)
		if err != nil {
			return err
		}
	}
	return nil
}

// expandDecl resolves the "[]" segments of a declaration path against the
// actual values, producing one concrete path per array element.
func expandDecl(decl kuboschema.ConnectionDecl, remaining []string, concrete []string, node interface{}, inContext bool, releaseNamespace string, generated *[]kubopackage.InputRendered, bindings *[]RefBinding) error {
	if len(remaining) == 0 {
		return emitDecl(decl, concrete, node, inContext, releaseNamespace, generated, bindings)
	}
	seg := remaining[0]
	if seg == "[]" {
		list, ok := node.([]interface{})
		if !ok {
			// Absent or not a list: nothing to expand (schema validation
			// already rejected wrong types)
			return nil
		}
		for i, item := range list {
			err := expandDecl(decl, remaining[1:], append(append([]string{}, concrete...), strconv.Itoa(i)), item, inContext, releaseNamespace, generated, bindings)
			if err != nil {
				return err
			}
		}
		return nil
	}
	nodeMap, ok := node.(map[string]interface{})
	if !ok {
		return nil
	}
	child, ok := nodeMap[seg]
	if !ok {
		// The field is absent. A selector has no deployer-provided value: its
		// slot (and the intermediate objects) must exist for the substitution.
		// For a ref: no name, no input, unless required (checked in emitDecl
		// through the empty-name path only when the field exists, so enforce
		// the required scalar case here as well).
		if decl.Selector {
			if len(remaining) == 1 {
				return emitDecl(decl, append(append([]string{}, concrete...), seg), nil, inContext, releaseNamespace, generated, bindings)
			}
			created := map[string]interface{}{}
			nodeMap[seg] = created
			return expandDecl(decl, remaining[1:], append(append([]string{}, concrete...), seg), created, inContext, releaseNamespace, generated, bindings)
		}
		if decl.Required && len(remaining) == 1 {
			return fmt.Errorf("parameter '%s' is required: no connection name provided", formatAlias(append(append([]string{}, concrete...), seg), inContext))
		}
		return nil
	}
	return expandDecl(decl, remaining[1:], append(append([]string{}, concrete...), seg), child, inContext, releaseNamespace, generated, bindings)
}

func emitDecl(decl kuboschema.ConnectionDecl, concrete []string, value interface{}, inContext bool, releaseNamespace string, generated *[]kubopackage.InputRendered, bindings *[]RefBinding) error {
	alias := formatAlias(concrete, inContext)
	if decl.Selector {
		if value != nil {
			return fmt.Errorf("parameter '%s' is a %s: it cannot be set by the release, the query lives in the package", alias, kuboschema.TypeConnectionSelector)
		}
		var ir kubopackage.InputRendered
		ir.Interface = decl.Interface
		ir.Alias = alias
		ir.Kind = kv1alpha1.Kind(decl.KindFilter)
		ir.AllowMultiple = true
		ir.Optional = !decl.Required
		ir.MatchLabels = decl.MatchLabels
		// Same rule as the ref branch below: a cluster-scoped lookup has no
		// namespace to search in
		if ir.Kind != kv1alpha1.KindClusterConnection {
			ir.InterfaceLookup.Namespace = releaseNamespace
		}
		*generated = append(*generated, ir)
		*bindings = append(*bindings, RefBinding{Alias: alias, Path: concrete, InContext: inContext, List: true})
		return nil
	}
	name, ok := value.(string)
	if !ok {
		return fmt.Errorf("parameter '%s' is a %s: it must carry a connection name (string), got %T", alias, kuboschema.TypeConnectionRef, value)
	}
	if name == "" {
		// Unset (or an empty templated default)
		if decl.Required {
			return fmt.Errorf("parameter '%s' is required: no connection name provided", alias)
		}
		return nil
	}
	var ir kubopackage.InputRendered
	ir.Interface = decl.Interface
	ir.Alias = alias
	ir.Kind = kv1alpha1.Kind(decl.KindFilter)
	ir.NamedConnection.Name = name
	if ir.Kind != kv1alpha1.KindClusterConnection {
		// A ClusterConnection is cluster-scoped: its lookup ignores the
		// namespace. Same rule as the hand-written stanza, where a namespace
		// set together with kind: ClusterConnection is an error.
		ir.NamedConnection.Namespace = releaseNamespace
	}
	// The deployer (or the Context) named this connection: always gate until
	// it is READY, whatever the schema-level required flag
	ir.Optional = false
	*generated = append(*generated, ir)
	*bindings = append(*bindings, RefBinding{Alias: alias, Path: concrete, InContext: inContext})
	return nil
}

// ApplyRefBindings substitutes the resolved values at the declaration points
// and removes the generated entries from the input model, so that .Inputs and
// .InputLists only expose the hand-written stanza. Must be called once the
// release is NOT gated: every non-optional generated input is resolved.
func ApplyRefBindings(bindings []RefBinding, inputModel, inputListModel, parameters, context map[string]interface{}) error {
	for _, b := range bindings {
		tree := parameters
		if b.InContext {
			tree = context
		}
		if b.List {
			list, ok := inputListModel[b.Alias]
			if !ok {
				// Optional selector with no match: an empty list
				list = []map[string]interface{}{}
			}
			if err := setAtPath(tree, b.Path, list); err != nil {
				return fmt.Errorf("could not substitute '%s': %w", b.Alias, err)
			}
		} else {
			values, ok := inputModel[b.Alias]
			if !ok {
				// Only possible for an unresolved input, which would have
				// gated the release before this point
				return fmt.Errorf("internal: no resolved values for '%s'", b.Alias)
			}
			if err := setAtPath(tree, b.Path, values); err != nil {
				return fmt.Errorf("could not substitute '%s': %w", b.Alias, err)
			}
		}
		delete(inputModel, b.Alias)
		delete(inputListModel, b.Alias)
	}
	return nil
}

func formatAlias(concrete []string, inContext bool) string {
	var sb strings.Builder
	if inContext {
		sb.WriteString("context")
	} else {
		sb.WriteString("parameters")
	}
	for _, seg := range concrete {
		if _, err := strconv.Atoi(seg); err == nil {
			sb.WriteString("[" + seg + "]")
		} else {
			sb.WriteString("." + seg)
		}
	}
	return sb.String()
}

// RenderRefDefaults renders the templated defaults of connectionRef
// parameters against the given model (which carries the Context). Called
// after the defaults merge: a remaining template string at a ref path can
// only come from the schema default, the deployer-provided parameters were
// already rendered. An empty rendering means "not set".
func RenderRefDefaults(decls []kuboschema.ConnectionDecl, parameters map[string]interface{}, model map[string]interface{}) error {
	for _, decl := range decls {
		if decl.Selector || decl.Default == "" {
			continue
		}
		if err := renderRefDefaultAt(decl.Path, nil, parameters, model); err != nil {
			return fmt.Errorf("default of connection parameter '%s': %w", strings.Join(decl.Path, "."), err)
		}
	}
	return nil
}

func renderRefDefaultAt(remaining, concrete []string, node interface{}, model map[string]interface{}) error {
	if len(remaining) == 0 {
		return nil
	}
	seg := remaining[0]
	if seg == "[]" {
		list, ok := node.([]interface{})
		if !ok {
			return nil
		}
		for i := range list {
			if err := renderRefDefaultAt(remaining[1:], append(append([]string{}, concrete...), strconv.Itoa(i)), list[i], model); err != nil {
				return err
			}
		}
		return nil
	}
	nodeMap, ok := node.(map[string]interface{})
	if !ok {
		return nil
	}
	if len(remaining) == 1 {
		value, ok := nodeMap[seg].(string)
		if !ok || !strings.Contains(value, "{{") {
			return nil
		}
		t, err := tmpl.New("", value, "")
		if err != nil {
			return err
		}
		rendered, err := t.RenderToSingleLine(model)
		if err != nil {
			return err
		}
		nodeMap[seg] = rendered
		return nil
	}
	return renderRefDefaultAt(remaining[1:], append(concrete, seg), nodeMap[seg], model)
}

// DeepCopyTree deep-copies a parameters or context tree (maps, lists and
// scalars). The defaults merge shares subtrees with the cached package
// defaults: substituting resolved connections in place without a copy would
// contaminate them across releases.
func DeepCopyTree(src map[string]interface{}) map[string]interface{} {
	out, _ := deepCopyValue(src).(map[string]interface{})
	return out
}

func deepCopyValue(src interface{}) interface{} {
	switch v := src.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for k, item := range v {
			out[k] = deepCopyValue(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, item := range v {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return src
	}
}

func setAtPath(tree map[string]interface{}, path []string, value interface{}) error {
	if len(path) == 0 {
		return fmt.Errorf("empty path")
	}
	var node interface{} = tree
	for _, seg := range path[:len(path)-1] {
		if idx, err := strconv.Atoi(seg); err == nil {
			list, ok := node.([]interface{})
			if !ok || idx >= len(list) {
				return fmt.Errorf("path step '%s': not a list or out of range", seg)
			}
			node = list[idx]
			continue
		}
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path step '%s': not a map", seg)
		}
		node = nodeMap[seg]
	}
	last := path[len(path)-1]
	if idx, err := strconv.Atoi(last); err == nil {
		list, ok := node.([]interface{})
		if !ok || idx >= len(list) {
			return fmt.Errorf("path step '%s': not a list or out of range", last)
		}
		list[idx] = value
		return nil
	}
	nodeMap, ok := node.(map[string]interface{})
	if !ok {
		return fmt.Errorf("path step '%s': not a map", last)
	}
	nodeMap[last] = value
	return nil
}
