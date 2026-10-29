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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"kubocd/internal/kubopackage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestHelmRepositoryArchivesCanBeFetched(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	chart := []byte("apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	if err := tw.WriteHeader(&tar.Header{Name: "demo/Chart.yaml", Mode: 0644, Size: int64(len(chart))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(chart); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	index := fmt.Sprintf(`apiVersion: v1
entries:
  demo:
  - apiVersion: v2
    name: demo
    version: 0.1.0
    digest: %x
    urls: [demo-0.1.0.tgz]
`, sha256.Sum256(archive.Bytes()))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.yaml":
			_, _ = w.Write([]byte(index))
		case "/demo-0.1.0.tgz":
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base := t.TempDir()
	assembly := filepath.Join(base, "assembly")
	if err := os.MkdirAll(assembly, 0755); err != nil {
		t.Fatal(err)
	}
	pck := &kubopackage.Package{}
	manifest := fmt.Sprintf(`modules:
- name: main
  type: HelmChart
  source:
    helmRepository:
      url: %q
      chart: demo
      version: 0.1.0
`, server.URL)
	if err := yaml.Unmarshal([]byte(manifest), pck); err != nil {
		t.Fatal(err)
	}
	archives, status, err := FetchArchives("", pck, assembly, filepath.Join(base, "work"), base)
	if err != nil {
		t.Fatalf("Helm repository chart could not be fetched: %v", err)
	}
	if len(archives) != 1 || len(status.ChartByModule) != 1 {
		t.Fatalf("unexpected archives or status: %v, %v", archives, status)
	}
	expected := kubopackage.ChartRef{Name: "demo", Version: "0.1.0"}
	if archives[0].Name != "demo-0.1.0.tgz" || status.ChartByModule["main"] != expected {
		t.Fatalf("unexpected chart metadata: %v, %v", archives, status)
	}
	data, err := os.ReadFile(filepath.Join(assembly, "demo-0.1.0.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, archive.Bytes()) {
		t.Fatal("Helm repository archive bytes changed")
	}
}
