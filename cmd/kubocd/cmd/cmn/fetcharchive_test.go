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

package cmn

import (
	"kubocd/internal/kubopackage"
	"testing"

	"sigs.k8s.io/yaml"
)

// kubocd render fetches the charts of a package whose grooming failed, which
// leaves the type and the source of a module unchecked.
func TestFetchArchivesRejectsAModuleItCannotFetch(t *testing.T) {
	cases := map[string]struct {
		module string
		want   string
	}{
		"Package module": {"type: Package\n    source: {oci: {repository: registry.example/demo, tag: 1.0.0}}",
			"module 'main': the module type 'Package' is not supported"},
		"unknown type": {"type: Helmchart\n    source: {oci: {repository: registry.example/demo, tag: 1.0.0}}",
			"module 'main': the module type 'Helmchart' is not supported"},
		"HelmChart module without a source": {"type: HelmChart", "module 'main': no source is set for its chart"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pck := &kubopackage.Package{}
			if err := yaml.Unmarshal([]byte("modules:\n  - name: main\n    "+c.module+"\n"), pck); err != nil {
				t.Fatal(err)
			}
			_, _, err := FetchArchives("", pck, t.TempDir(), t.TempDir(), t.TempDir())
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
		})
	}
}
