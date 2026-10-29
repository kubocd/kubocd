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
package cmd

import (
	"kubocd/cmd/kubocd/cmd/cmn"
	"kubocd/cmd/kubocd/cmd/tgz"
	"kubocd/internal/misc"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssemblyReadsStayInAssembly(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	assembly, err := misc.PrepareDir(root, "assembly")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"original.yaml", "groomed.yaml", "index.yaml", "status.yaml", "chart.tgz"} {
		if err := assembly.WriteFile(name, []byte("trusted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.WriteFile("outside.tgz", []byte("outside-read-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root.Path(), "outside.tgz")
	err = buildAssembly(assembly, []cmn.ArchiveInfo{{Name: "../outside.tgz", Path: outside}})
	if err == nil || !strings.Contains(err.Error(), "trusted parent") {
		t.Fatalf("assembly read escaped its source: %v", err)
	}
	err = buildAssembly(assembly, []cmn.ArchiveInfo{{Name: "chart.tgz", Path: outside}})
	if err != nil {
		t.Fatal(err)
	}
	content, err := tgz.ExtractDataAt(assembly, "assembly.tgz", "chart.tgz")
	if err != nil || string(content) != "trusted" {
		t.Fatalf("assembly trusted the host path: %s %v", content, err)
	}
}
