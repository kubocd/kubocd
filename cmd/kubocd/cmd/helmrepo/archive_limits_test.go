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
package helmrepo

import (
	"bytes"
	"errors"
	"kubocd/internal/archiveio"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"strings"
	"testing"

	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartloader "helm.sh/helm/v4/pkg/chart/v2/loader"
)

func TestCLIArchiveHelmDumpLimits(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	raw := testarchive.Raw(t, testarchive.Entry{Name: "file", Data: "12345"})
	if err := root.WriteFile("chart.tgz", testarchive.Gzip(t, raw), 0600); err != nil {
		t.Fatal(err)
	}
	l := archiveio.Defaults()
	l.ContentSize = 4
	op := &Operation{Output: t.TempDir(), ChartName: "budget-chart", ChartVersion: "1", ArchiveLimits: &l}
	if err := dumpChartAt(root, "chart.tgz", op); !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("Helm dump ignored limit: %v", err)
	}
	l.ContentSize = 5
	if err := dumpChartAt(root, "chart.tgz", op); err != nil {
		t.Fatal(err)
	}
}

func TestCLIArchiveHelmListingLimits(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	raw := testarchive.Raw(t, testarchive.Entry{Name: "file"})
	if err := root.WriteFile("chart.tgz", testarchive.Gzip(t, raw), 0600); err != nil {
		t.Fatal(err)
	}
	l := archiveio.Defaults()
	l.ListingSize = 4
	info := &chart.Chart{Metadata: &chart.Metadata{Name: "budget-chart", Version: "1"}}
	err = dumpFetchedChartAt(root, "chart.tgz", info, &Operation{ArchiveLimits: &l})
	if !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("Helm listing ignored limit: %v", err)
	}
}

func TestCLIArchiveHelmMetadataLimits(t *testing.T) {
	root, err := misc.OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	metadata := "apiVersion: v2\nname: demo\nversion: 1.0.0\n"
	raw := testarchive.Raw(t,
		testarchive.Entry{Name: "demo/charts/child/Chart.yaml", Data: "name: child\n"},
		testarchive.Entry{Name: "demo/Chart.yaml", Data: metadata})
	if err := root.WriteFile("chart.tgz", testarchive.Gzip(t, raw), 0600); err != nil {
		t.Fatal(err)
	}
	l := archiveio.Defaults()
	l.MetadataSize = int64(len("name: child\n"))
	info := &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}}
	op := &Operation{ArchiveLimits: &l}
	if err := dumpFetchedChartAt(root, "chart.tgz", info, op); !errors.Is(err, archiveio.ErrLimit) {
		t.Fatalf("Helm dump ignored metadata limit: %v", err)
	}
	l.MetadataSize = int64(len(metadata))
	if err := dumpFetchedChartAt(root, "chart.tgz", info, op); err != nil {
		t.Fatal(err)
	}
	l.MetadataSize = 1
	l.Disabled = true
	if err := dumpFetchedChartAt(root, "chart.tgz", info, op); err != nil {
		t.Fatal(err)
	}
}

func TestCLIArchiveHelmMixedSeparators(t *testing.T) {
	metadata := "apiVersion: v2\nname: demo\nversion: 1.0.0\n"
	large := metadata + "description: " + strings.Repeat("x", 1024) + "\n"
	for _, kind := range []string{"duplicate oversized root", "mixed root only"} {
		t.Run(kind, func(t *testing.T) {
			root, err := misc.OpenRootedDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Root.Close() }()
			entries := []testarchive.Entry{{Name: `demo/prefix\Chart.yaml`, Data: metadata}}
			if kind == "duplicate oversized root" {
				entries = []testarchive.Entry{{Name: "demo/Chart.yaml", Data: metadata},
					{Name: `demo/prefix\Chart.yaml`, Data: large}}
			}
			data := testarchive.Gzip(t, testarchive.Raw(t, entries...))
			loaded, err := chartloader.LoadArchive(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("Helm did not accept mixed-separator fixture: %v", err)
			}
			if err := root.WriteFile("chart.tgz", data, 0600); err != nil {
				t.Fatal(err)
			}
			limits := archiveio.Defaults()
			limits.MetadataSize = int64(len(metadata))
			op := &Operation{ArchiveLimits: &limits}
			err = dumpFetchedChartAt(root, "chart.tgz", loaded, op)
			if kind == "duplicate oversized root" {
				if !errors.Is(err, archiveio.ErrLimit) {
					t.Fatalf("mixed-separator root metadata escaped its limit: %v", err)
				}
				limits.MetadataSize = int64(len(large))
				if err := dumpFetchedChartAt(root, "chart.tgz", loaded, op); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatalf("Helm root metadata was not found: %v", err)
			}
			got, err := readDumpChartMetadata(root, "chart.tgz", &limits)
			if err != nil || got.Name != loaded.Metadata.Name || got.Description != loaded.Metadata.Description {
				t.Fatalf("bounded metadata differs from Helm: %v, %v", got, err)
			}
		})
	}
}
