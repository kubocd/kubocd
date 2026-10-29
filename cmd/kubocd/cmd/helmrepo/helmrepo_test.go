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

package helmrepo

import (
	"kubocd/internal/misc"
	"os"
	"path/filepath"
	"strings"
	"testing"

	helmclient "github.com/mittwald/go-helm-client"
)

func TestDumpChartContainsOutput(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, ".transient"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root.Path(), "demo-1")); err != nil {
		t.Fatal(err)
	}
	err = dumpChartAt(root, "missing.tgz", &Operation{Output: root.Path(), ChartName: "demo", ChartVersion: "1"})
	callErr := err
	info, err := os.Lstat(filepath.Join(root.Path(), "demo-1"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("output symlink was removed")
	}
	if callErr == nil || !strings.Contains(callErr.Error(), "symlink") {
		t.Fatalf("output symlink was not refused before extraction: %v", callErr)
	}
}

func TestHelmContentCacheStaysInWorkDirectory(t *testing.T) {
	work := t.TempDir()
	// An unsupported scheme stops setup after client creation without opening a socket.
	_, client, err := SetupHelmRepo(&Operation{WorkDir: work, RepoUrl: "unsupported://repository"}, "cache-test")
	if err == nil || !strings.Contains(err.Error(), "could not find protocol handler for: unsupported") {
		t.Fatalf("expected unsupported repository scheme: %v", err)
	}
	concrete, ok := client.(*helmclient.HelmClient)
	if !ok {
		t.Fatalf("client was not constructed: %T", client)
	}
	expected := filepath.Join(work, "helmRepo", ".helmcontent")
	if concrete.Settings.ContentCache != expected {
		t.Fatalf("content cache is outside the prepared work directory: got %q, want %q",
			concrete.Settings.ContentCache, expected)
	}
}
