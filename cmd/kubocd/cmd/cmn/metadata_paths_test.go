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

//nolint:goconst // Keep the hostile paths beside their assertions.
package cmn

import (
	"fmt"
	"kubocd/internal/kubopackage"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestFetchedChartMetadataCannotEscapeAssembly(t *testing.T) {
	for _, tc := range []struct{ name, version string }{
		{"../../victim/demo", "0.1.0"}, {"demo", "../../../victim/x"},
	} {
		t.Run(tc.name+tc.version, func(t *testing.T) {
			base := t.TempDir()
			assembly := filepath.Join(base, "work", "assembly")
			sourceWrite(t, filepath.Join(assembly, ".transient"), "")
			destination := filepath.Join(assembly, fmt.Sprintf("%s-%s.tgz", tc.name, tc.version))
			sourceWrite(t, destination, "sentinel")
			chart := filepath.Join(base, "chart")
			sourceChart(t, chart)
			sourceWrite(t, filepath.Join(chart, "Chart.yaml"), fmt.Sprintf(
				"apiVersion: v2\nname: %q\nversion: %q\n", tc.name, tc.version))
			sourceHelm(t, ":")
			pck := &kubopackage.Package{}
			manifest := fmt.Sprintf("modules:\n- name: main\n  type: HelmChart\n  source:\n    local:\n      path: %q\n", chart)
			if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
				t.Fatal(err)
			}
			_, _, err := FetchArchives("", pck, assembly, filepath.Join(base, "work"), base)
			data, readErr := os.ReadFile(destination)
			if readErr != nil || string(data) != "sentinel" {
				t.Fatal("outside sentinel changed")
			}
			if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("metadata path was not refused: %v", err)
			}
		})
	}
	t.Run("healthy and deduplicated", func(t *testing.T) {
		base := t.TempDir()
		chart := filepath.Join(base, "chart")
		sourceChart(t, chart)
		sourceHelm(t, ":")
		pck := &kubopackage.Package{}
		manifest := "modules:\n"
		for _, name := range []string{"one", "two"} {
			manifest += fmt.Sprintf(
				"- name: %s\n  type: HelmChart\n  source:\n    local:\n      path: %q\n", name, chart)
		}
		if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
			t.Fatal(err)
		}
		sourceWrite(t, filepath.Join(base, "assembly", ".transient"), "")
		archives, status, err := FetchArchives("", pck, filepath.Join(base, "assembly"), filepath.Join(base, "work"), base)
		if err != nil {
			t.Fatal(err)
		}
		if len(archives) != 1 || len(status.ChartByModule) != 2 {
			t.Fatal("chart deduplication changed")
		}
		expected, err := os.ReadFile(filepath.Join(chart, "Chart.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if sourceContents(t, archives[0].Path)["one/Chart.yaml"] != string(expected) {
			t.Fatal("healthy chart bytes changed")
		}
	})

}

func TestModuleArchiveNameCannotEscapeWork(t *testing.T) {
	for _, gitSource := range []bool{false, true} {
		t.Run(fmt.Sprint(gitSource), func(t *testing.T) {
			base := t.TempDir()
			work := filepath.Join(base, "work", "nested")
			chart := filepath.Join(base, "chart")
			sourceChart(t, chart)
			sourceHelm(t, ":")
			module := "../../../victim/m"
			destination := filepath.Join(base, "victim", "m.tgz")
			sourceWrite(t, destination, "sentinel")
			var err error
			if gitSource {
				sourceRepo(t, chart)
				_, err = getHelmChartArchiveFromGit("", chart, "main", "", ".", module, work, nil)
			} else {
				_, err = getHelmCharArchiveFromLocal("", chart, module, work)
			}
			data, readErr := os.ReadFile(destination)
			if readErr != nil || string(data) != "sentinel" {
				t.Fatal("outside sentinel changed")
			}
			if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("module path was not refused: %v", err)
			}
		})
	}
}
