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

package kuboschema

import (
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// schemaOf decodes a schema written in YAML.
func schemaOf(t *testing.T, text string) KuboSchema {
	t.Helper()
	schema := KuboSchema{}
	if err := yaml.Unmarshal([]byte(text), &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestAnArrayKeepsItsDefault(t *testing.T) {
	schema := schemaOf(t, `
$schema: http://json-schema.org/schema#
type: object
properties:
  tags:
    type: array
    items:
      type: string
    default: [a, b]
  names:
    type: array
    items:
      type: string
  nested:
    type: object
    properties:
      list:
        type: array
        items:
          type: string
        default: [x]
  replicas:
    type: integer
    default: 2
`)
	got, err := Defaulter(schema)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"tags":     []interface{}{"a", "b"},
		"names":    []interface{}{},
		"nested":   map[string]interface{}{"list": []interface{}{"x"}},
		"replicas": float64(2),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got the defaults %v, want %v", got, want)
	}
}

func TestTheKuboCDFormatRefusesTheDefaultOfAnArray(t *testing.T) {
	schema := schemaOf(t, `
properties:
  tags:
    type: array
    items:
      type: string
    default: [a]
`)
	_, err := Kubo2openAPI(schema, false)
	if err == nil || !strings.Contains(err.Error(), "unknown property 'default'") {
		t.Fatalf("got error %v, want the default of an array refused", err)
	}
}
