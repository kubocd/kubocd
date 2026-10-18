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
	"kubocd/internal/misc"
	"reflect"
	"testing"

	"sigs.k8s.io/yaml"
)

const extensionsPackage = `
apiVersion: v1alpha1
name: podinfo
tag: 1.0.0
modules:
  - name: main
    source:
      oci: {repository: ghcr.io/stefanprodan/charts/podinfo, tag: "6.9.2"}
extensions:
  example.org:
    nested: {list: [a, b], flag: true}
`

func TestExtensionsAreCarried(t *testing.T) {
	pck := &Package{}
	if err := misc.YamlDecode(extensionsPackage, pck); err != nil {
		t.Fatalf("strict decoding: %v", err)
	}
	if err := pck.Groom(nil); err != nil {
		t.Fatalf("grooming: %v", err)
	}
	var groomed Package
	if err := yaml.Unmarshal(misc.Map2Json(pck), &groomed); err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"nested": map[string]interface{}{"list": []interface{}{"a", "b"}, "flag": true}}
	if !reflect.DeepEqual(groomed.Extensions["example.org"], want) {
		t.Errorf("groomed extensions: %v", groomed.Extensions)
	}
}
