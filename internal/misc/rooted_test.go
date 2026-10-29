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

//nolint:goconst // Keep paths and sentinel contents beside their cases.
package misc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rootedFixture(t *testing.T) RootedDir {
	t.Helper()
	d, err := OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Root.Close() })
	return d
}

func TestRootedCleanupProtectsParentAndMarker(t *testing.T) {
	for _, name := range []string{".", "a/..", "../victim", "alias", "away"} {
		t.Run(name, func(t *testing.T) {
			d := rootedFixture(t)
			if err := d.WriteFile(".transient", nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.WriteFile("sentinel", []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.Root.Symlink(".", "alias"); err != nil {
				t.Fatal(err)
			}
			if err := d.Root.Symlink(t.TempDir(), "away"); err != nil {
				t.Fatal(err)
			}
			err := SafeRemoveAt(d, name)
			data, readErr := d.ReadFile("sentinel")
			if readErr != nil || string(data) != "keep" {
				t.Fatal("trusted parent was removed")
			}
			if err == nil {
				t.Fatal("unsafe cleanup target was accepted")
			}
		})
	}
	for _, kind := range []string{"link", "directory", "missing"} {
		t.Run(kind, func(t *testing.T) {
			d := rootedFixture(t)
			child, err := PrepareDir(d, "child")
			if err != nil {
				t.Fatal(err)
			}
			if err := child.WriteFile("sentinel", []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.Root.Remove("child/.transient"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "link":
				if err := d.Root.Symlink("sentinel", "child/.transient"); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := child.MkdirAll(".transient", 0755); err != nil {
					t.Fatal(err)
				}
			}
			err = SafeEnsureEmptyAt(d, "child")
			data, readErr := child.ReadFile("sentinel")
			if readErr != nil || string(data) != "keep" {
				t.Fatal("invalid marker authorized cleanup")
			}
			if err == nil {
				t.Fatal("invalid marker was accepted")
			}
		})
	}
	t.Run("healthy", func(t *testing.T) {
		d := rootedFixture(t)
		child, err := PrepareDir(d, "nested/child")
		if err != nil {
			t.Fatal(err)
		}
		if err := child.WriteFile("old", []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := SafeEnsureEmptyAt(d, "nested/child"); err != nil {
			t.Fatal(err)
		}
		if _, err := child.ReadFile("old"); !os.IsNotExist(err) {
			t.Fatalf("marked directory was not emptied: %v", err)
		}
		if err := SafeRemoveAt(d, "nested/child"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRootedOperationsContainLinks(t *testing.T) {
	for _, operation := range []string{"read", "write", "copy"} {
		t.Run(operation, func(t *testing.T) {
			d := rootedFixture(t)
			child, err := PrepareDir(d, "child")
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.Root.Symlink(outside, "child/link"); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "read":
				var data []byte
				data, err = child.ReadFile("link")
				if string(data) == "keep" {
					t.Fatal("outside bytes were read")
				}
			case "write":
				err = child.WriteFile("link", []byte("changed"), 0600)
			case "copy":
				err = child.CopyFrom("copy", child, "link")
			}
			data, readErr := os.ReadFile(outside)
			if readErr != nil || string(data) != "keep" {
				t.Fatal("outside sentinel changed")
			}
			if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("outgoing link was accepted: %v", err)
			}
		})
	}
	t.Run("assigned parent", func(t *testing.T) {
		d := rootedFixture(t)
		child, err := PrepareDir(d, "child")
		if err != nil {
			t.Fatal(err)
		}
		if err := d.WriteFile("sentinel", []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := d.Root.Symlink("../sentinel", "child/link"); err != nil {
			t.Fatal(err)
		}
		if _, err := child.ReadFile("link"); err == nil {
			t.Fatal("read escaped its assigned subtree")
		}
		if _, err := child.Child("../sibling"); err == nil {
			t.Fatal("child escaped its assigned subtree")
		}
	})
	t.Run("internal link", func(t *testing.T) {
		d := rootedFixture(t)
		if err := d.WriteFile("value", []byte("healthy"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := d.Root.Symlink("value", "link"); err != nil {
			t.Fatal(err)
		}
		data, err := d.ReadFile("link")
		if err != nil || string(data) != "healthy" {
			t.Fatalf("internal link failed: %s %v", data, err)
		}
	})
}

func TestRootedResolvesLinkBeforeParent(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "nested/deeper"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"payload.yaml": "A", "nested/payload.yaml": "B"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"alias": "nested/deeper", "link": "alias/../payload.yaml"} {
		if err := os.Symlink(target, filepath.Join(base, name)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := OpenRootedDir(base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	for _, name := range []string{"link", "alias/../payload.yaml"} {
		got, err := root.ReadFile(name)
		if err != nil || string(got) != "B" {
			t.Fatalf("link/parent changed rooted bytes: %q, %v", got, err)
		}
	}
	if err := root.WriteFile("link", []byte("C"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(base, "nested/payload.yaml"))
	if err != nil || string(got) != "C" {
		t.Fatalf("link/parent wrote wrong target: %q, %v", got, err)
	}
}

func TestRootedCleanupRejectsResolvedParent(t *testing.T) {
	for _, operation := range []string{"prepare", "remove", "child"} {
		for _, name := range []string{"a/link/..", "a/link/../.."} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				root := rootedFixture(t)
				parent, err := PrepareDir(root, "modules")
				if err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{"a", "b"} {
					if err := parent.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if err := parent.WriteFile("sentinel", []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := parent.Root.Symlink("../b", filepath.Join(parent.Rel, "a", "link")); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "prepare":
					_, err = PrepareDir(parent, name)
				case "remove":
					err = SafeRemoveAt(parent, name)
				case "child":
					_, err = parent.Child(name)
				}
				got, readErr := parent.ReadFile("sentinel")
				if readErr != nil || string(got) != "keep" {
					t.Fatalf("resolved cleanup removed its parent sentinel: %q, %v", got, readErr)
				}
				if err == nil {
					t.Fatal("resolved parent was accepted as a child")
				}
			})
		}
	}
}
