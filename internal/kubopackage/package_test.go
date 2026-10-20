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
	return pck.Groom(nil)
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
