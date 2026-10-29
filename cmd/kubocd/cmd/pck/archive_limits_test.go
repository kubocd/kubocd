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
package pck

import (
	"errors"
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/internal/archiveio"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIArchiveDumpPackageLimits(t *testing.T) {
	for _, kind := range []string{"outer", "original.yaml", "groomed.yaml", "status.yaml", "nested", "healthy"} {
		t.Run(kind, func(t *testing.T) {
			work, err := misc.OpenRootedDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = work.Root.Close() }()
			original := `apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    local:
      path: ./chart
`
			status := "chartByModule:\n  main:\n    name: demo\n    version: 1\n"
			chart := testarchive.Gzip(t, testarchive.Raw(t, testarchive.Entry{Name: "file", Data: strings.Repeat("x", 4097)}))
			files := map[string][]byte{"original.yaml": []byte(original), "groomed.yaml": []byte(original),
				"status.yaml": []byte(status), "demo-1.tgz": chart}
			l := archiveio.Defaults()
			switch kind {
			case "outer":
				l.ContentSize = 1
			case "original.yaml", "groomed.yaml", "status.yaml":
				l.MetadataSize = 1024
				files[kind] = []byte(string(files[kind]) + "#" + strings.Repeat("x", 1025))
			case "nested":
				l.ContentSize = 4096
			}
			if err := work.WriteFile("package.tgz", packageArchive(t, files), 0600); err != nil {
				t.Fatal(err)
			}
			err = dumpPackageArchive(work, "package.tgz", t.TempDir(), true, &l)
			if kind == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, archiveio.ErrLimit) {
				t.Fatalf("dump package ignored %s limit: %v", kind, err)
			}
			if kind == "outer" {
				entries, readErr := work.ReadDir("manifest.tar")
				if readErr != nil {
					t.Fatal(readErr)
				}
				for _, entry := range entries {
					if entry.Name() != ".transient" {
						t.Fatal("outer package was extracted past limit")
					}
				}
			}
			l.Disabled = true
			if err := dumpPackageArchive(work, "package.tgz", t.TempDir(), true, &l); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("local", func(t *testing.T) {
		manifest, base := testarchive.LocalChart(t, 1025)
		l := archiveio.Defaults()
		l.MetadataSize = 1024
		err := Dump(manifest, filepath.Join(base, "work"), false, false, true, filepath.Join(base, "out"),
			cmn.SourceOptions{ArchiveLimits: &l})
		if !errors.Is(err, archiveio.ErrLimit) {
			t.Fatalf("local dump ignored metadata limit: %v", err)
		}
	})
}
