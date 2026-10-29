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
package tgz

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"kubocd/internal/archiveio"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIArchiveExtractionBudgets(t *testing.T) {
	for _, kind := range []string{"content", "entries", "stream"} {
		t.Run(kind, func(t *testing.T) {
			l := archiveio.Defaults()
			switch kind {
			case "content":
				l.ContentSize = 4
			case "entries":
				l.Entries = 1
			case "stream":
				l.StreamSize = 512
			}
			raw := testarchive.Raw(t, testarchive.Entry{Name: "a/file", Data: "12345"})
			target, err := misc.OpenRootedDir(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = target.Root.Close() }()
			err = ExtractAllFromReader(bytes.NewReader(testarchive.Gzip(t, raw)), target, &l)
			if !errors.Is(err, archiveio.ErrLimit) {
				t.Fatalf("extraction bypassed %s limit: %v", kind, err)
			}
			if _, err := os.Stat(filepath.Join(target.Path(), "a/file")); err == nil && kind != "stream" {
				t.Fatal("offending entry was created")
			}
		})
	}
}

func TestCLIArchiveMetadataConsumers(t *testing.T) {
	for _, file := range []string{"Chart.yaml", "original.yaml", "groomed.yaml", "status.yaml"} {
		t.Run(file, func(t *testing.T) {
			l := archiveio.Defaults()
			l.MetadataSize = 1024
			data := "key: " + strings.Repeat("x", 1020)
			raw := testarchive.Raw(t, testarchive.Entry{Name: "prefix/" + file, Data: data})
			name := testarchive.Write(t, testarchive.Gzip(t, raw))
			root, err := misc.OpenRootedDir(filepath.Dir(name))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Root.Close() }()
			var sink bytes.Buffer
			var decoded map[string]interface{}
			for _, call := range []func() error{
				func() error { return ExtractFromTgz(name, file, &sink, &l) },
				func() error {
					data, err := ExtractDataFromTgz(name, file, &l)
					if data != nil {
						t.Fatal("oversized metadata was returned")
					}
					return err
				},
				func() error { return UnmarshalDataFromTgz(name, file, &decoded, &l) },
				func() error {
					data, err := ExtractDataAt(root, filepath.Base(name), file, &l)
					if data != nil {
						t.Fatal("oversized metadata was returned")
					}
					return err
				},
				func() error { return UnmarshalDataAt(root, filepath.Base(name), file, &decoded, &l) },
			} {
				if err := call(); !errors.Is(err, archiveio.ErrLimit) {
					t.Fatalf("metadata limit bypassed: %v", err)
				}
			}
			if sink.Len() != 0 || len(decoded) != 0 {
				t.Fatal("metadata delivered before size check")
			}
			for _, disabled := range []bool{false, true} {
				l.Disabled = disabled
				if !disabled {
					l.MetadataSize = 1025
				} else {
					l.MetadataSize = 1
				}
				got, err := ExtractDataFromTgz(name, file, &l)
				if err != nil || string(got) != data {
					t.Fatalf("raised/disabled metadata limit failed: %v", err)
				}
			}
		})
	}
}

func TestCLIArchiveMetadataCountsSkippedContent(t *testing.T) {
	for _, matchFirst := range []bool{false, true} {
		entries := []testarchive.Entry{{Name: "skip", Data: strings.Repeat("x", 4096)}, {Name: "Chart.yaml", Data: "x"}}
		if matchFirst {
			entries[0], entries[1] = entries[1], entries[0]
		}
		raw := testarchive.Raw(t, entries...)
		l := archiveio.Defaults()
		l.ContentSize = 4096
		err := ExtractFromReader(bytes.NewReader(testarchive.Gzip(t, raw)), "Chart.yaml", io.Discard, &l)
		if !errors.Is(err, archiveio.ErrLimit) {
			t.Fatalf("metadata scan ignored over-budget tail or skipped content: %v", err)
		}
	}
}

func TestCLIArchiveDisabledKeepsPathProtection(t *testing.T) {
	l := archiveio.Defaults()
	l.Disabled = true
	raw := testarchive.Raw(t, testarchive.Entry{Name: "../escape"})
	err := ExtractAllFromTgz(testarchive.Write(t, testarchive.Gzip(t, raw)), t.TempDir(), &l)
	if err == nil || !strings.Contains(err.Error(), "not a relative path") {
		t.Fatalf("disabled limits bypassed path check: %v", err)
	}
	raw = testarchive.Raw(t, testarchive.Entry{Name: "link", Type: tar.TypeSymlink},
		testarchive.Entry{Name: "file", Data: "x"})
	if err := ExtractAllFromTgz(testarchive.Write(t, testarchive.Gzip(t, raw)), t.TempDir(), &l); err != nil {
		t.Fatal(err)
	}
}
