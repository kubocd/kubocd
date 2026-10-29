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
package misc

import (
	"bytes"
	"errors"
	"kubocd/internal/archiveio"
	"kubocd/internal/testarchive"
	"strings"
	"testing"
)

func TestCLIArchiveListingBudget(t *testing.T) {
	for _, n := range []int{127, 128, 129} {
		limits := archiveio.Defaults()
		limits.ListingSize = 128
		raw := testarchive.Raw(t, testarchive.Entry{Name: strings.Repeat("x", n-1)})
		got, err := ListTarGzContentsReader(bytes.NewReader(testarchive.Gzip(t, raw)), &limits)
		if n <= 128 {
			if err != nil || len(got) != n {
				t.Fatalf("listing boundary failed: %d, %v", len(got), err)
			}
			continue
		}
		if got != "" || !errors.Is(err, archiveio.ErrLimit) {
			t.Fatalf("oversized listing was returned: %q, %v", got, err)
		}
		if err.Error() != "archive listing exceeds 128 bytes" {
			t.Fatal(err)
		}
	}
}

func TestCLIArchiveListingOtherBudgets(t *testing.T) {
	for _, kind := range []string{"content", "entries", "stream"} {
		t.Run(kind, func(t *testing.T) {
			l := archiveio.Defaults()
			switch kind {
			case "content":
				l.ContentSize = 1
			case "entries":
				l.Entries = 1
			case "stream":
				l.StreamSize = 512
			}
			raw := testarchive.Raw(t, testarchive.Entry{Name: "a/file", Data: "xx"})
			got, err := ListTarGzContentsReader(bytes.NewReader(testarchive.Gzip(t, raw)), &l)
			if got != "" || !errors.Is(err, archiveio.ErrLimit) {
				t.Fatalf("listing bypassed %s limit: %q, %v", kind, got, err)
			}
		})
	}
}

func TestCLIArchiveDiskMetadataBudget(t *testing.T) {
	root, err := OpenRootedDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Root.Close() }()
	for _, name := range []string{"original.yaml", "groomed.yaml", "status.yaml"} {
		data := "key: " + strings.Repeat("x", 1020)
		if err := root.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		l := archiveio.Defaults()
		l.MetadataSize = 1024
		var value map[string]interface{}
		err := LoadArchiveYaml(root, name, &l, &value)
		if !errors.Is(err, archiveio.ErrLimit) || len(value) != 0 {
			t.Fatalf("oversized disk metadata was decoded: %v", err)
		}
		l.MetadataSize = 1025
		if err := LoadArchiveYaml(root, name, &l, &value); err != nil {
			t.Fatal(err)
		}
	}
}
