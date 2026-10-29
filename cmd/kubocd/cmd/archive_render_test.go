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
package cmd

import (
	"errors"
	"kubocd/internal/archiveio"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIArchiveRenderLimits(t *testing.T) {
	saved := renderParams.sourceOptions
	defer func() { renderParams.sourceOptions = saved }()
	for _, kind := range []string{"original.yaml", "status.yaml", "outer", "nested", "local"} {
		t.Run(kind, func(t *testing.T) {
			work, err := misc.OpenRootedDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = work.Root.Close() }()
			output, err := misc.OpenRootedDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = output.Root.Close() }()
			l := archiveio.Defaults()
			renderParams.sourceOptions.ArchiveLimits = &l
			original := "name: demo\ntag: 1\n"
			statusText := "chartByModule:\n  main:\n    name: demo\n    version: 1\n"
			status := &kubopackage.Status{ChartByModule: map[string]kubopackage.ChartRef{"main": {Name: "demo", Version: "1"}}}
			if kind == "original.yaml" {
				original += "#" + strings.Repeat("x", 1025)
			}
			if kind == "status.yaml" {
				statusText += "#" + strings.Repeat("x", 1025)
			}
			nested := testarchive.Gzip(t, testarchive.Raw(t, testarchive.Entry{Name: "file", Data: strings.Repeat("x", 4097)}))
			raw := testarchive.Raw(t, testarchive.Entry{Name: "original.yaml", Data: original},
				testarchive.Entry{Name: "status.yaml", Data: statusText},
				testarchive.Entry{Name: "demo-1.tgz", Data: string(nested)})
			if err := work.WriteFile("package.tgz", testarchive.Gzip(t, raw), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "original.yaml", "status.yaml":
				l.MetadataSize = 1024
				err = readRenderPackageMetadata(work, "package.tgz", &kubopackage.Package{}, &kubopackage.Status{})
			case "outer", "nested":
				l.ContentSize = 4096
				if kind == "outer" {
					l.ContentSize = 1
				}
				err = extractRenderPackage(work, "package.tgz", output, status)
			case "local":
				manifest, base := testarchive.LocalChart(t, 1025)
				pkg := &kubopackage.Package{}
				if err := misc.LoadYaml(manifest, pkg); err != nil {
					t.Fatal(err)
				}
				pkg.Modules[0].Type = "HelmChart"
				l.MetadataSize = 1024
				container := &kubopackage.PckContainer{Package: pkg}
				err = fetchRenderCharts(work, output, container, base)
			}
			if !errors.Is(err, archiveio.ErrLimit) {
				t.Fatalf("render ignored %s limit: %v", kind, err)
			}
			if kind == "outer" {
				entries, readErr := work.ReadDir("manifest.tar")
				if readErr != nil {
					t.Fatal(readErr)
				}
				for _, entry := range entries {
					if entry.Name() != ".transient" {
						t.Fatal("outer render package was extracted past limit")
					}
				}
			}
		})
	}
}

func TestCLIArchivePackageCommandLimits(t *testing.T) {
	saved := packageParams
	defer func() { packageParams = saved }()
	manifest, base := testarchive.LocalChart(t, 1025)
	l := archiveio.Defaults()
	l.MetadataSize = 1024
	packageParams.sourceOptions.ArchiveLimits = &l
	packageParams.workDir = filepath.Join(base, "work")
	// Invalid registry syntax prevents any network attempt if a mutation drops the archive limit.
	packageParams.ociRepoPrefix = "invalid registry!"
	if err := pack(manifest); !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("package command ignored archive limit: %v", err)
	}
}
