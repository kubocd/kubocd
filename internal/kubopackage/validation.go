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

package kubopackage

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// describedErrorTypes are the gojsonschema error types whose description
// quotes the schema only, never the validated value.
var describedErrorTypes = map[string]bool{
	"required": true, "invalid_type": true, "false": true, "missing_dependency": true,
	"number_any_of": true, "number_one_of": true, "number_all_of": true, "number_not": true,
	"condition_then": true, "condition_else": true, "contains": true, "unique": true,
	"array_no_additional_items": true, "array_min_items": true, "array_max_items": true,
	"array_min_properties": true, "array_max_properties": true, "string_gte": true, "string_lte": true,
	"pattern": true, "format": true, "multiple_of": true,
	"number_gte": true, "number_gt": true, "number_lte": true, "number_lt": true,
}

// SchemaError describes a failed validation of instance against schema by the
// location and the category of an error, without the values or the property
// names of instance: a template can produce both from data. The descriptions
// are sorted, so that the same failure always reports the same one.
func SchemaError(result *gojsonschema.Result, schema map[string]interface{}, instance interface{}) string {
	descriptions := make([]string, 0, len(result.Errors()))
	for _, e := range result.Errors() {
		descriptions = append(descriptions, schemaPath(e.Context(), schema, instance)+": "+schemaReason(e))
	}
	slices.Sort(descriptions)
	return descriptions[0]
}

// schemaPath locates a validation error by the property names the schema
// declares and by list indexes. It stops before the first property name the
// schema does not declare, which comes from the data.
func schemaPath(context *gojsonschema.JsonContext, schema map[string]interface{}, instance interface{}) string {
	if context == nil {
		return "(root)"
	}
	// The first segment is the root of the context
	segments := strings.Split(context.String("\x00"), "\x00")[1:]
	path := make([]string, 0, len(segments))
	node, value := schema, instance
	for _, segment := range segments {
		if list, ok := value.([]interface{}); ok {
			items, _ := node["items"].(map[string]interface{})
			index, err := strconv.Atoi(segment)
			if items == nil || err != nil || index < 0 || index >= len(list) || strconv.Itoa(index) != segment {
				break
			}
			node, value = items, list[index]
		} else {
			properties, _ := node["properties"].(map[string]interface{})
			child, declared := properties[segment].(map[string]interface{})
			object, ok := value.(map[string]interface{})
			if !declared || !ok {
				break
			}
			node, value = child, object[segment]
		}
		path = append(path, segment)
	}
	if len(path) == 0 {
		return "(root)"
	}
	return strings.Join(path, ".")
}

// schemaReason gives the category of a validation error, with what the
// schema expects.
func schemaReason(e gojsonschema.ResultError) string {
	switch e.Type() {
	case "additional_property_not_allowed":
		return "a property is not declared in the schema"
	case "invalid_property_pattern", "invalid_property_name":
		return "a property name does not match the schema"
	case "enum":
		return fmt.Sprintf("must be one of the following: %v", e.Details()["allowed"])
	case "const":
		return fmt.Sprintf("must be %v", e.Details()["allowed"])
	}
	if describedErrorTypes[e.Type()] {
		return e.Description()
	}
	return fmt.Sprintf("does not satisfy the schema (%s)", e.Type())
}
