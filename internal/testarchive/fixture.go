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

package testarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type Entry struct {
	Name   string
	Data   string
	Type   byte
	Format tar.Format
}

func Raw(t *testing.T, entries ...Entry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, entry := range entries {
		kind := entry.Type
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: entry.Name, Typeflag: kind, Mode: 0600, Size: int64(len(entry.Data)), Format: entry.Format}
		if kind == tar.TypeSymlink || kind == tar.TypeLink {
			h.Linkname = "target"
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.Data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func Gzip(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func Write(t *testing.T, data []byte) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.tgz")
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func LocalChart(t *testing.T, metadataSize int) (string, string) {
	t.Helper()
	base := t.TempDir()
	chart := filepath.Join(base, "chart")
	if err := os.MkdirAll(chart, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := "apiVersion: v2\nname: demo\nversion: 0.1.0\n#"
	if metadataSize < len(metadata) {
		metadataSize = len(metadata)
	}
	metadata += strings.Repeat("x", metadataSize-len(metadata))
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifest := filepath.Join(base, "package.yaml")
	content := fmt.Sprintf("apiVersion: v1alpha1\nname: demo\ntag: 0.1.0\nmodules:\n- name: main\n  source:\n    local:\n      path: %q\n", chart)
	if err := os.WriteFile(manifest, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return manifest, base
}
