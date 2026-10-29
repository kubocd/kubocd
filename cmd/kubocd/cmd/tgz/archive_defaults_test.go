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
	"bytes"
	"fmt"
	"kubocd/internal/misc"
	"kubocd/internal/testarchive"
	"strings"
	"testing"
)

func TestCLIArchiveDefaultLimits(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		data := testarchive.Gzip(t, testarchive.Raw(t, testarchive.Entry{
			Name: "demo/Chart.yaml", Data: strings.Repeat("x", (1<<20)+1)}))
		got, err := ExtractDataFromTgz(testarchive.Write(t, data), "Chart.yaml")
		if err == nil || got != nil || !strings.Contains(err.Error(), "archive metadata exceeds 1048576 bytes") {
			t.Fatalf("oversized metadata was returned: %d bytes, %v", len(got), err)
		}
	})
	t.Run("entries", func(t *testing.T) {
		entries := make([]testarchive.Entry, 1001)
		for i := range entries {
			entries[i].Name = fmt.Sprintf("f%d", i)
		}
		data := testarchive.Gzip(t, testarchive.Raw(t, entries...))
		name := testarchive.Write(t, data)
		err := ExtractAllFromTgz(name, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "archive entries exceed 1000") {
			t.Fatalf("over-budget entries were extracted: %v", err)
		}
		listing, err := misc.ListTarGzContents(name)
		if err == nil || listing != "" {
			t.Fatal("over-budget entries were listed")
		}
	})
}

func TestMetadataSearchValidatesRemainingArchive(t *testing.T) {
	raw := testarchive.Raw(t, testarchive.Entry{Name: "demo/Chart.yaml", Data: "first"})
	raw[1024] = 'x'
	data, err := ExtractDataFromTgz(testarchive.Write(t, testarchive.Gzip(t, raw)), "Chart.yaml")
	if err == nil || data != nil {
		t.Fatalf("metadata search ignored corrupt tail: %q, %v", data, err)
	}
}

func TestMetadataSearchKeepsFirstMatch(t *testing.T) {
	raw := testarchive.Raw(t, testarchive.Entry{Name: "demo/Chart.yaml", Data: "first"},
		testarchive.Entry{Name: "nested/Chart.yaml", Data: "second"})
	name := testarchive.Write(t, testarchive.Gzip(t, raw))
	data, err := ExtractDataFromTgz(name, "Chart.yaml")
	if err != nil || !bytes.Equal(data, []byte("first")) {
		t.Fatalf("first match changed: %q, %v", data, err)
	}
	_, err = ExtractDataFromTgz(name, "missing.yaml")
	if err == nil || err.Error() != "file 'missing.yaml' not found in archive" {
		t.Fatalf("not found changed: %v", err)
	}
}
