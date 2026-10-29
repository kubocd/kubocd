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
	"context"
	"errors"
	"fmt"
	"kubocd/cmd/kubocd/cmd/helmrepo"
	"kubocd/internal/archiveio"
	"kubocd/internal/kubopackage"
	"kubocd/internal/testarchive"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestCLIArchiveHTTPRepositoryLimits(t *testing.T) {
	chart := testarchive.Gzip(t, testarchive.Raw(t, testarchive.Entry{
		Name: "budget-chart/Chart.yaml", Data: "apiVersion: v2\nname: budget-chart\nversion: 2.3.4\n"}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(`apiVersion: v1
entries:
  budget-chart:
  - apiVersion: v2
    name: budget-chart
    version: 2.3.4
    urls: [budget-chart.tgz]
`))
		case "/budget-chart.tgz":
			_, _ = w.Write(chart)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	pck := &kubopackage.Package{}
	manifest := fmt.Sprintf(`modules:
- name: archive-limited-fetch
  type: HelmChart
  source:
    helmRepository:
      url: %q
      chart: budget-chart
      version: 2.3.4
`, server.URL)
	if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
		t.Fatal(err)
	}
	l := archiveio.Defaults()
	l.MetadataSize = 1
	_, _, err := FetchArchives("", pck, t.TempDir(), t.TempDir(), t.TempDir(), SourceOptions{ArchiveLimits: &l})
	if !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("HTTP chart ignored metadata limit: %v", err)
	}
	l.Disabled = true
	pck.Modules[0].Name = "archive-unlimited-fetch"
	if _, _, err := FetchArchives("", pck, t.TempDir(), t.TempDir(), t.TempDir(),
		SourceOptions{ArchiveLimits: &l}); err != nil {
		t.Fatal(err)
	}
	// DumpHelmRepo uses the fixed alias "local" in Helm's process-global repository storage.
	for _, mode := range []string{"limited", "raised", "metadata-limited", "metadata-raised", "metadata-disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCLIArchiveHTTPDumpProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), "KUBOCD_TEST_HTTP_DUMP_URL="+server.URL, "KUBOCD_TEST_HTTP_DUMP_MODE="+mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("HTTP dump %s: %v\n%s", mode, err, output)
			}
		})
	}
}

func TestCLIArchiveHTTPDumpProcess(t *testing.T) {
	url := os.Getenv("KUBOCD_TEST_HTTP_DUMP_URL")
	if url == "" {
		return
	}
	l := archiveio.Defaults()
	mode := os.Getenv("KUBOCD_TEST_HTTP_DUMP_MODE")
	switch mode {
	case "limited":
		l.ListingSize = 1
	case "raised":
		l.ListingSize = 1024
	case "metadata-limited":
		l.MetadataSize = 1
	case "metadata-raised":
		l.MetadataSize = 1024
	case "metadata-disabled":
		l.MetadataSize = 1
		l.Disabled = true
	default:
		t.Fatalf("unknown HTTP dump mode %q", mode)
	}
	op := &helmrepo.Operation{WorkDir: t.TempDir(), RepoUrl: url,
		ChartName: "budget-chart", ChartVersion: "2.3.4", ArchiveLimits: &l}
	err := helmrepo.DumpHelmRepo(op)
	if mode == "limited" || mode == "metadata-limited" {
		if !errors.Is(err, archiveio.ErrLimit) {
			t.Fatalf("HTTP dump ignored %s limit: %v", mode, err)
		}
	} else if err != nil {
		t.Fatalf("HTTP dump refused raised listing limit: %v", err)
	}
}
