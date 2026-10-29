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

//nolint:goconst // Fixture names are clearer beside their cases.
package misc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyChartTreeUsesRootedReads(t *testing.T) {
	for _, kind := range []string{"external", "internal", "absolute-internal", "cycle", "dangling", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sub", "content"), []byte("marker"), 0600); err != nil {
				t.Fatal(err)
			}
			target := "sub/content"
			switch kind {
			case "external":
				target = filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
					t.Fatal(err)
				}
			case "absolute-internal":
				target = filepath.Join(dir, target)
			case "cycle":
				target = "."
			case "dangling":
				target = "missing"
			case "directory":
				target = "sub"
			}
			if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			source, err := OpenChartSource(dir, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			dest, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dest.Close() }()
			err = source.CopyTree(".", dest, ".", false)
			if kind == "external" || kind == "cycle" || kind == "dangling" {
				if err == nil {
					t.Fatal("unsafe source link was copied")
				}
				want := "dangling or cyclic"
				if kind == "external" {
					want = "outside its trusted root"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("got %v, want %s", err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			name := "link"
			if kind == "directory" {
				name += "/content"
			}
			info, err := dest.Lstat(name)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("link was not materialized as a regular file: %v", err)
			}
			data, err := dest.ReadFile(name)
			if err != nil || string(data) != "marker" {
				t.Fatalf("materialized content: %q, %v", data, err)
			}
		})
	}
}

func TestChartSourceResolvesLinkBeforeParent(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{"chart/templates", "nested/deeper"} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"payload.yaml": "A", "nested/payload.yaml": "B"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"alias": "nested/deeper", "chart/templates/config.yaml": "../../alias/../payload.yaml"} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, allow := range []bool{false, true} {
		source, err := OpenChartSource(base, allow)
		if err != nil {
			t.Fatal(err)
		}
		target, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		err = source.CopyTree("chart", target, "chart", false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := target.ReadFile("chart/templates/config.yaml")
		if err != nil || string(got) != "B" {
			t.Fatalf("link/parent changed source bytes: %q, %v", got, err)
		}
		_ = target.Close()
		_ = source.Close()
	}
}
