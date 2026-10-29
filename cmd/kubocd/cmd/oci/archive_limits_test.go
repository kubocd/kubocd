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
package oci

import (
	"errors"
	"kubocd/internal/archiveio"
	"kubocd/internal/global"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
)

func TestCLIArchiveOCIDumpLimits(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	raw := testarchive.Raw(t, testarchive.Entry{Name: "file", Data: "12345"})
	data := testarchive.Gzip(t, raw)
	value := digest.FromBytes(data)
	name, err := blobName(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, chart := range []bool{false, true} {
		l := archiveio.Defaults()
		l.ListingSize = 4
		op := &Operation{WorkDir: root.Path(), Chart: chart, Output: t.TempDir(),
			ImageRepo: "demo", ImageTag: "1", ArchiveLimits: &l}
		if err := dumpEntry("", global.HelmChartMediaType, value, op); !errors.Is(err, archiveio.ErrLimit) {
			t.Fatalf("OCI listing ignored limit: %v", err)
		}
		l.ListingSize = 5
		if err := dumpEntry("", global.HelmChartMediaType, value, op); err != nil {
			t.Fatal(err)
		}
	}
}
