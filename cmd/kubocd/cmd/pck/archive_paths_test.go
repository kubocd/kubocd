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

//nolint:goconst // Fixture paths are intentionally repeated across boundary assertions.
package pck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"kubocd/internal/misc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func packageArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestDumpPackageArchiveContainsMetadataPaths(t *testing.T) {
	for _, kind := range []string{"package", "module", "reference", "version", "healthy"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			work, err := misc.OpenRootedDir(filepath.Join(base, "work"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = work.Root.Close() }()
			output := filepath.Join(base, "output")
			victim := filepath.Join(base, "victim")
			if err := os.MkdirAll(victim, 0755); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string]string{".transient": "", "sentinel": "keep"} {
				if err := os.WriteFile(filepath.Join(victim, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			name, module, ref, version := "demo", "main", "demo", "0.1.0"
			switch kind {
			case "package":
				name = "../victim"
			case "module":
				module = "../../../MESC/m"
			case "reference":
				ref = "../../victim/demo"
			case "version":
				version = "../../../victim/x"
			}
			original := fmt.Sprintf("apiVersion: v1alpha1\nname: %q\ntag: 0.1.0\n"+
				"modules:\n- name: main\n  source:\n    local:\n      path: ./chart\n", name)
			status := fmt.Sprintf("chartByModule:\n  %q:\n    name: %q\n    version: %q\n", module, ref, version)
			chart := packageArchive(t, map[string][]byte{
				"demo/Chart.yaml": []byte("apiVersion: v2\nname: demo\nversion: 0.1.0\n")})
			if kind == "reference" || kind == "version" {
				outside := filepath.Join(work.Path(), "manifest.tar", fmt.Sprintf("%s-%s.tgz", ref, version))
				if err := os.MkdirAll(filepath.Dir(outside), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(outside, chart, 0600); err != nil {
					t.Fatal(err)
				}
			}
			archive := packageArchive(t, map[string][]byte{"original.yaml": []byte(original), "groomed.yaml": []byte(original),
				"status.yaml": []byte(status), "demo-0.1.0.tgz": chart})
			if err := work.WriteFile("package.tgz", archive, 0600); err != nil {
				t.Fatal(err)
			}
			err = dumpPackageArchive(work, "package.tgz", output, true)
			sentinel, readErr := os.ReadFile(filepath.Join(victim, "sentinel"))
			if readErr != nil || string(sentinel) != "keep" {
				t.Fatal("outside sentinel changed")
			}
			if kind == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(output, "demo", "charts", "main", "demo", "Chart.yaml"))
				if err != nil || string(got) != "apiVersion: v2\nname: demo\nversion: 0.1.0\n" {
					t.Fatalf("healthy chart bytes changed: %s %v", got, err)
				}
				return
			}
			if _, err := os.Stat(filepath.Join(base, "MESC", "m")); err == nil {
				t.Fatal("escaped path was created")
			}
			if kind == "reference" || kind == "version" {
				if _, err := os.Stat(filepath.Join(output, "demo", "charts", "main", "demo", "Chart.yaml")); err == nil {
					t.Fatal("outside archive was read")
				}
			}
			if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("package metadata path was not refused: %v", err)
			}
		})
	}
}
