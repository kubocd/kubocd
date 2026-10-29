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

package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestDumpChartContainsOutput(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "Chart.yaml", Mode: 0600, Size: 0}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	value := digest.FromBytes(data.Bytes())
	name, err := blobName(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(name, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, ".transient"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Root.Symlink(outside, "demo-1"); err != nil {
		t.Fatal(err)
	}
	op := &Operation{WorkDir: root.Path(), Output: root.Path(), Chart: true, ImageRepo: "registry/demo", ImageTag: "1"}
	err = dumpEntry("fixture", global.HelmChartMediaType, value, op)
	callErr := err
	info, err := os.Lstat(filepath.Join(root.Path(), "demo-1"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("output symlink was removed")
	}
	if callErr == nil || !strings.Contains(callErr.Error(), "symlink") {
		t.Fatalf("output symlink was not refused: %v", callErr)
	}
}

func TestOCIBlobReferencesAreValidated(t *testing.T) {
	for _, value := range []digest.Digest{"bad", "sha256:../../victim", "../sha256:abc"} {
		t.Run(string(value), func(t *testing.T) {
			if _, err := blobName(value); err == nil {
				t.Fatal("invalid digest was accepted")
			}
		})
	}
}
