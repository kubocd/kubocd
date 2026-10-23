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

package kubopackage

import (
	"fmt"
	"kubocd/internal/tmpl"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func groomManifest(t *testing.T, manifest string) error {
	t.Helper()
	pck := &Package{}
	if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
		t.Fatalf("invalid test manifest: %v", err)
	}
	return pck.Groom(nil, tmpl.PackageFunctions(false))
}

func TestConditionsParseErrorNamesTheField(t *testing.T) {
	err := groomManifest(t, `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
conditions: "{{ if }}"
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
`)
	if err == nil || !strings.Contains(err.Error(), "'conditions'") {
		t.Fatalf("expected an error naming 'conditions', got %v", err)
	}
}

func TestExcludedFunctionInPackageIsRejected(t *testing.T) {
	err := groomManifest(t, `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      ca: {{ readFile "/etc/ssl/ca.crt" }}
`)
	if err == nil {
		t.Fatal("grooming should fail")
	}
	for _, want := range []string{"module 'main'", "'values'", `function "readFile" not defined`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestPackageCallingResolveDNSIsAccepted(t *testing.T) {
	err := groomManifest(t, `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      ip: {{ resolveDNS .Parameters.host }}
`)
	if err != nil {
		t.Fatalf("a package calling resolveDNS should groom: %v", err)
	}
}

func TestParseErrorInABlockHeaderNamesBothPlaces(t *testing.T) {
	err := groomManifest(t, `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
templateHeader: |-
  {{- $host := .Context.host }}
  {{- $now := now }}
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      host: {{ $host }}
`)
	want := `could not parse 'values' template: in templateHeader or on the first line of the template: template: :2: function "now" not defined`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

func TestAModuleTypeOtherThanHelmChartOrPackageIsRejected(t *testing.T) {
	manifest := `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
  - name: main
    type: %s
    source:
      oci:
        repository: registry.example/demo
        tag: 1.0.0
`
	for moduleType, want := range map[string]string{
		"HelmChart": "",
		"Package":   "",
		"Helmchart": "module 'main': 'type' must be HelmChart or Package (is 'Helmchart')",
	} {
		t.Run(moduleType, func(t *testing.T) {
			err := groomManifest(t, fmt.Sprintf(manifest, moduleType))
			if (want == "" && err != nil) || (want != "" && (err == nil || err.Error() != want)) {
				t.Fatalf("got error %v, want %q", err, want)
			}
		})
	}
}
