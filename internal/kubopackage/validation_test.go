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

//nolint:goconst // test inputs read better inline
package kubopackage

import (
	"fmt"
	"kubocd/internal/tmpl"
	"testing"

	"sigs.k8s.io/yaml"
)

const canary = "s3cr3t-SEC1-canary"

func containerWithSchema(t *testing.T, schema string) *PckContainer {
	t.Helper()
	pck := &Package{}
	manifest := fmt.Sprintf(`
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
%s
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
`, schema)
	if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
		t.Fatalf("invalid test manifest: %v", err)
	}
	container := &PckContainer{}
	if err := container.SetPackage(pck, nil, "0.1.0@sha256:0123456789abcdef", nil, tmpl.PackageFunctions(false)); err != nil {
		t.Fatalf("invalid test package: %v", err)
	}
	return container
}

func TestSchemaErrorsDoNotQuoteTheData(t *testing.T) {
	cases := map[string]struct {
		schema string
		params map[string]interface{}
		want   string
	}{
		"undeclared property": {`
  parameters:
    properties:
      replicas:
        type: integer`, map[string]interface{}{"replicas": 1, canary: "x"}, "(root): a property is not declared in the schema"},
		"undeclared nested property": {`
  parameters:
    properties:
      db:
        properties:
          host:
            type: string`, map[string]interface{}{"db": map[string]interface{}{"host": "h", canary: "x"}}, "db: a property is not declared in the schema"},
		"value of an undeclared property": {`
  parameters:
    $schema: http://json-schema.org/schema#
    type: object
    properties:
      labels:
        type: object
        additionalProperties:
          type: integer`, map[string]interface{}{"labels": map[string]interface{}{canary: canary}}, "labels: Invalid type. Expected: integer, given: string"},
		"property name pattern": {`
  parameters:
    $schema: http://json-schema.org/schema#
    type: object
    patternProperties:
      "^s3":
        type: object
        properties:
          port:
            type: integer`, map[string]interface{}{canary: map[string]interface{}{"port": canary}}, "(root): Invalid type. Expected: integer, given: string"},
		"enum": {`
  parameters:
    properties:
      mode:
        type: string
        enum: [fast, safe]`, map[string]interface{}{"mode": canary}, `mode: must be one of the following: "fast", "safe"`},
		"list item": {`
  parameters:
    properties:
      hosts:
        type: array
        items:
          properties:
            name:
              type: string
              required: true`, map[string]interface{}{"hosts": []interface{}{map[string]interface{}{"name": "a"}, map[string]interface{}{canary: "x"}}}, "hosts.1: a property is not declared in the schema"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := containerWithSchema(t, c.schema).ValidateParameters(c.params)
			if want := "parameters schema validation error: " + c.want; err == nil || err.Error() != want {
				t.Fatalf("got error %v, want %q", err, want)
			}
		})
	}
}

func TestContextSchemaErrorDoesNotQuoteTheData(t *testing.T) {
	container := containerWithSchema(t, `
  context:
    properties:
      ingress:
        additionalProperties: false
        properties:
          className:
            type: string`)
	err := container.ValidateContext(map[string]interface{}{"ingress": map[string]interface{}{canary: "x"}})
	if want := "context schema validation error: ingress: a property is not declared in the schema"; err == nil || err.Error() != want {
		t.Fatalf("got error %v, want %q", err, want)
	}
}

// Kubo2openAPI lists the required properties in map order: the reported
// error must not depend on it.
func TestSchemaErrorIsTheSameAtEachLoad(t *testing.T) {
	schema := `
  parameters:
    properties:
      alpha:
        type: string
        required: true
      beta:
        type: string
        required: true
      gamma:
        type: string
        required: true`
	for range 20 {
		err := containerWithSchema(t, schema).ValidateParameters(map[string]interface{}{})
		if want := "parameters schema validation error: (root): alpha is required"; err == nil || err.Error() != want {
			t.Fatalf("got error %v, want %q", err, want)
		}
	}
}
