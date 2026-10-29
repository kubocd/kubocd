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

//nolint:goconst // Keep archive fixtures and boundary diagnostics beside their assertions.
package cmn

import (
	"errors"
	"fmt"
	"kubocd/internal/archiveio"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestCLIArchiveFetchLimits(t *testing.T) {
	for _, gitSource := range []bool{false, true} {
		t.Run(fmt.Sprint(gitSource), func(t *testing.T) {
			manifest, base := testarchive.LocalChart(t, 1025)
			pck := &kubopackage.Package{}
			if err := misc.LoadYaml(manifest, pck); err != nil {
				t.Fatal(err)
			}
			pck.Modules[0].Type = "HelmChart"
			if gitSource {
				chart := filepath.Join(base, "chart")
				sourceRepo(t, chart)
				raw := fmt.Sprintf(`modules:
- name: main
  type: HelmChart
  source:
    git:
      url: %q
      branch: main
      path: .
`, chart)
				if err := yaml.Unmarshal([]byte(raw), pck); err != nil {
					t.Fatal(err)
				}
			}
			l := archiveio.Defaults()
			l.MetadataSize = 1024
			assembly := filepath.Join(base, "assembly")
			_, _, err := FetchArchives("", pck, assembly, filepath.Join(base, "work"), base, SourceOptions{ArchiveLimits: &l})
			if !errors.Is(err, archiveio.ErrLimit) {
				t.Fatalf("chart fetch ignored metadata limit: %v", err)
			}
			if _, err := os.Stat(filepath.Join(assembly, "demo-0.1.0.tgz")); err == nil {
				t.Fatal("over-budget chart copied into assembly")
			}
			l.MetadataSize = 1025
			if _, _, err := FetchArchives("", pck, assembly, filepath.Join(base, "work"), base,
				SourceOptions{ArchiveLimits: &l}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCLIArchivePackageChartExtractionLimits(t *testing.T) {
	manifest, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manifest.Root.Close() }()
	output, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Root.Close() }()
	raw := testarchive.Raw(t, testarchive.Entry{Name: "file", Data: "12345"})
	if err := manifest.WriteFile("budget-chart-1.tgz", testarchive.Gzip(t, raw), 0600); err != nil {
		t.Fatal(err)
	}
	status := &kubopackage.Status{ChartByModule: map[string]kubopackage.ChartRef{
		"main": {Name: "budget-chart", Version: "1"}}}
	l := archiveio.Defaults()
	l.ContentSize = 4
	if err := ExtractPackageCharts(manifest, output, status, &l); !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("nested chart ignored limit: %v", err)
	}
}
