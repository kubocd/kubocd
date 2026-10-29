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
package archiveio

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"kubocd/internal/testarchive"
	"math"
	"strings"
	"testing"
)

func scan(t *testing.T, raw []byte, limits *Limits) error {
	t.Helper()
	r, err := New(bytes.NewReader(testarchive.Gzip(t, raw)), limits)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for {
		_, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func requireLimit(t *testing.T, err error, message string) {
	t.Helper()
	if !errors.Is(err, ErrLimit) || err.Error() != message {
		t.Fatalf("expected %q, got %v", message, err)
	}
}

func TestCLIArchiveContentBudget(t *testing.T) {
	for _, size := range []int{4095, 4096, 4097} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			limits := Defaults()
			limits.ContentSize = 4096
			raw := testarchive.Raw(t, testarchive.Entry{Name: "file", Data: strings.Repeat("x", size)})
			err := scan(t, raw, &limits)
			if size <= 4096 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("over-budget content was accepted")
			}
			requireLimit(t, err, "archive content exceeds 4096 bytes")
		})
	}
	limits := Defaults()
	limits.ContentSize = 4096
	raw := testarchive.Raw(t, testarchive.Entry{Name: "same", Data: strings.Repeat("x", 2048)},
		testarchive.Entry{Name: "same", Data: strings.Repeat("x", 2049)})
	requireLimit(t, scan(t, raw, &limits), "archive content exceeds 4096 bytes")
}

func TestCLIArchiveStreamBudget(t *testing.T) {
	limits := Defaults()
	limits.StreamSize = 4096
	for _, size := range []int{4095, 4096, 4097} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			raw := testarchive.Raw(t, testarchive.Entry{Name: "file", Data: "x"})
			raw = append(raw, make([]byte, size-len(raw))...)
			err := scan(t, raw, &limits)
			if size <= 4096 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("over-budget stream was accepted")
			}
			requireLimit(t, err, "archive decompressed stream exceeds 4096 bytes")
		})
	}
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		raw := testarchive.Raw(t, testarchive.Entry{Name: strings.Repeat("x", 5000), Format: format})
		requireLimit(t, scan(t, raw, &limits), "archive decompressed stream exceeds 4096 bytes")
	}
	raw := testarchive.Raw(t, testarchive.Entry{Name: "skip", Data: strings.Repeat("x", 4096)})
	requireLimit(t, scan(t, raw, &limits), "archive decompressed stream exceeds 4096 bytes")
}

func TestCLIArchiveEntryBudget(t *testing.T) {
	limits := Defaults()
	limits.Entries = 8
	for _, n := range []int{7, 8, 9} {
		entries := make([]testarchive.Entry, n)
		for i := range entries {
			entries[i].Name = "same"
		}
		err := scan(t, testarchive.Raw(t, entries...), &limits)
		if n <= 8 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatal("over-budget entries were accepted")
		}
		requireLimit(t, err, "archive entries exceed 8")
	}
	t.Run("implicit", func(t *testing.T) {
		err := scan(t, testarchive.Raw(t, testarchive.Entry{Name: "a/b/c/d/e/f/g/h/file"}), &limits)
		if err == nil {
			t.Fatal("implicit directories were not counted")
		}
		requireLimit(t, err, "archive entries exceed 8")
	})
	t.Run("explicit after implicit", func(t *testing.T) {
		limits.Entries = 5
		entries := []testarchive.Entry{{Name: "a/b/file"}, {Name: "a/b/", Type: tar.TypeDir}, {Name: "a/", Type: tar.TypeDir}}
		if err := scan(t, testarchive.Raw(t, entries...), &limits); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("skipped types", func(t *testing.T) {
		limits.Entries = 2
		raw := testarchive.Raw(t, testarchive.Entry{Name: "link", Type: tar.TypeSymlink},
			testarchive.Entry{Name: "hard", Type: tar.TypeLink}, testarchive.Entry{Name: "fifo", Type: tar.TypeFifo})
		requireLimit(t, scan(t, raw, &limits), "archive entries exceed 2")
	})
}

func TestCLIArchiveMetadataBudget(t *testing.T) {
	for _, n := range []int{1023, 1024, 1025} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			limits := Defaults()
			limits.MetadataSize = 1024
			raw := testarchive.Raw(t, testarchive.Entry{Name: "original.yaml", Data: strings.Repeat("x", n)})
			r, err := New(bytes.NewReader(testarchive.Gzip(t, raw)), &limits)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			if _, err := r.Next(); err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			err = r.CopyMetadata(&b)
			if n <= 1024 {
				if err != nil || b.Len() != n {
					t.Fatalf("metadata boundary failed: %d, %v", b.Len(), err)
				}
				return
			}
			if err == nil || b.Len() != 0 {
				t.Fatal("oversized metadata was returned")
			}
			requireLimit(t, err, "archive metadata exceeds 1024 bytes")
		})
	}
}

func TestCLIArchiveLimitsValidation(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, n := range []int64{-1, 0, math.MaxInt64/2 + 1} {
			for _, field := range []string{"stream", "content", "metadata", "listing", "entries"} {
				l := Defaults()
				l.Disabled = disabled
				switch field {
				case "stream":
					l.StreamSize = n
				case "content":
					l.ContentSize = n
				case "metadata":
					l.MetadataSize = n
				case "listing":
					l.ListingSize = n
				case "entries":
					l.Entries = n
				}
				if l.Validate() == nil {
					t.Fatalf("invalid %s limit %d was accepted", field, n)
				}
			}
		}
	}
	l := Defaults()
	l.Entries = 1048577
	if l.Validate() == nil {
		t.Fatal("overflowing entry limit accepted")
	}
	l = Defaults()
	l.StreamSize = math.MaxInt64 / 2
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCLIArchiveDisabledLimitsStillValidateArchive(t *testing.T) {
	l := Defaults()
	l.StreamSize = 1
	l.ContentSize = 1
	l.Entries = 1
	l.MetadataSize = 1
	l.ListingSize = 1
	l.Disabled = true
	raw := testarchive.Raw(t, testarchive.Entry{Name: "a/b/file", Data: "healthy"})
	if err := scan(t, raw, &l); err != nil {
		t.Fatal(err)
	}
	if err := scan(t, raw[:len(raw)-1024], &l); err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("missing tar terminator was accepted: %v", err)
	}
}

func TestCLIArchiveSparseLogicalContent(t *testing.T) {
	for _, size := range []int64{4095, 4096, 4097, math.MaxInt64/2 + 1} {
		raw := testarchive.Raw(t, testarchive.Entry{Name: "sparse", Format: tar.FormatGNU})
		raw[156] = tar.TypeGNUSparse
		copy(raw[483:495], fmt.Sprintf("%011o\x00", size))
		if size > 4097 {
			// The GNU base-256 field represents large logical files without storing their holes.
			for i := 483; i < 495; i++ {
				raw[i] = 0
			}
			for i, n := 494, uint64(size); n != 0; i, n = i-1, n>>8 {
				raw[i] = byte(n)
			}
			raw[483] |= 0x80
		}
		for i := 148; i < 156; i++ {
			raw[i] = ' '
		}
		sum := 0
		for _, b := range raw[:512] {
			sum += int(b)
		}
		copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
		l := Defaults()
		l.ContentSize = 4096
		err := scan(t, raw, &l)
		if size <= 4096 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatal("over-budget sparse content was accepted")
		}
		requireLimit(t, err, "archive content exceeds 4096 bytes")
	}
}

func TestCLIArchiveMetadataGrowthIsBounded(t *testing.T) {
	l := Defaults()
	l.MetadataSize = 1024
	data, err := ReadMetadata(strings.NewReader(strings.Repeat("x", 1025)), 1, &l)
	if data != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("growing disk metadata was returned: %d, %v", len(data), err)
	}
}

func TestCLIArchiveStreamStopsAtLimit(t *testing.T) {
	l := Defaults()
	l.StreamSize = 1024
	b := &budget{limits: l}
	input := &countedReader{Reader: bytes.NewReader(make([]byte, 4096))}
	r := &streamReader{input: input, budget: b}
	_, err := io.Copy(io.Discard, r)
	requireLimit(t, err, "archive decompressed stream exceeds 1024 bytes")
	if input.count > 1025 {
		t.Fatalf("stream read beyond its one-byte probe: %d", input.count)
	}
}

type countedReader struct {
	io.Reader
	count int
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += n
	return n, err
}
