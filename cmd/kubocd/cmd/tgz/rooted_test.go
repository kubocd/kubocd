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

package tgz

import (
	"kubocd/internal/misc"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootedExtractionContainsItsTargetAndSource(t *testing.T) {
	archive := writeTgz(t, regular("chart/Chart.yaml", chartYaml))
	base := t.TempDir()
	source, err := misc.OpenRootedDir(filepath.Dir(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Root.Close() }()
	root, err := misc.OpenRootedDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	for _, name := range []string{"../escape", filepath.Join(t.TempDir(), "escape")} {
		if _, err := root.Child(name); err == nil {
			t.Fatal("escaping extraction target was accepted")
		}
	}
	target, err := root.Child("target")
	if err != nil {
		t.Fatal(err)
	}
	if err := target.MkdirAll(".", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(target.Path(), "chart")); err != nil {
		t.Fatal(err)
	}
	if err := ExtractAllAt(source, filepath.Base(archive), target); err == nil {
		t.Fatal("outgoing extraction link was accepted")
	}
	rel, err := filepath.Rel(root.Path(), archive)
	if err != nil {
		t.Fatal(err)
	}
	err = ExtractAllAt(root, rel, target)
	if err == nil || !strings.Contains(err.Error(), "trusted parent") {
		t.Fatalf("outside archive was read: %v", err)
	}
	if err := os.Remove(filepath.Join(target.Path(), "chart")); err != nil {
		t.Fatal(err)
	}
	if err := ExtractAllAt(source, filepath.Base(archive), target); err != nil {
		t.Fatal(err)
	}
	data, err := target.ReadFile("chart/Chart.yaml")
	if err != nil || string(data) != chartYaml {
		t.Fatalf("healthy extraction changed bytes: %s %v", data, err)
	}

}

func TestRootedExtractionContainsTargetPrefix(t *testing.T) {
	archive := writeTgz(t, regular("chart/Chart.yaml", chartYaml))
	source, err := misc.OpenRootedDir(filepath.Dir(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Root.Close() }()
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()

	t.Run("target prefix", func(t *testing.T) {
		alias, err := root.Child("alias")
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := root.Root.Symlink(outside, "alias"); err != nil {
			t.Fatal(err)
		}
		err = ExtractAllAt(source, filepath.Base(archive), alias)
		if _, statErr := os.Stat(filepath.Join(outside, "chart", "Chart.yaml")); statErr == nil {
			t.Fatal("extraction wrote outside target")
		}
		if err == nil {
			t.Fatal("symlink extraction prefix was accepted")
		}
	})
}
