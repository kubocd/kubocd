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

package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// get returns the status code and the body of a GET of url, after redirects.
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	answer, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = answer.Body.Close() }()
	body, err := io.ReadAll(answer.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer.StatusCode, string(body)
}

func TestTheChartServerServesItsFilesAndListsNoFolder(t *testing.T) {
	const (
		chartNamespaceDirectory = "apps"
		chartReleaseDirectory   = "demo"
	)
	root := t.TempDir()
	folder := filepath.Join(root, "hr", chartNamespaceDirectory, chartReleaseDirectory)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{ociTestIndex: "apiVersion: v1\nentries: {}\n", "demo-1.0.0.tgz": "the chart archive"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(folder, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(helmRepositoryHandler(root, logr.Discard()))
	defer server.Close()
	for name, content := range files {
		if code, body := get(t, server.URL+"/hr/apps/demo/"+name); code != http.StatusOK || body != content {
			t.Errorf("%s: got %d %q, want 200 %q", name, code, body, content)
		}
	}
	folders := []string{"/", "/hr", "/hr/", "/hr/apps", "/hr/apps/", "/hr/apps/demo", "/hr/apps/demo/",
		"/hr/apps/demo/index.html"}
	for _, path := range append(folders, "/hr/apps/demo/missing.yaml") {
		code, body := get(t, server.URL+path)
		if code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, code)
		}
		for _, name := range []string{chartNamespaceDirectory, chartReleaseDirectory, ociTestIndex} {
			if strings.Contains(body, name) {
				t.Errorf("%s: the answer names %s:\n%s", path, name, body)
			}
		}
	}
}
