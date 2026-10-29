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

//nolint:goconst // Keep fixture values beside their assertions.
package pck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDumpPackageContainsMetadataPaths(t *testing.T) {
	base := t.TempDir()
	output := filepath.Join(base, "output")
	victim := filepath.Join(base, "victim")
	for _, dir := range []string{output, victim} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{".transient": "", "sentinel": "keep"} {
		if err := os.WriteFile(filepath.Join(victim, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(victim, filepath.Join(output, "demo")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(base, "package.yaml")
	content := "apiVersion: v1alpha1\nname: demo\ntag: 0.1.0\n" +
		"modules:\n- name: main\n  source:\n    local:\n      path: ./chart\n"
	if err := os.WriteFile(manifest, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	err := Dump(manifest, filepath.Join(base, "work"), false, false, false, output)
	data, readErr := os.ReadFile(filepath.Join(victim, "sentinel"))
	if readErr != nil || string(data) != "keep" {
		t.Fatal("outside sentinel changed")
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("package path was not refused: %v", err)
	}
}
