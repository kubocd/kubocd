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

package misc

import (
	"archive/tar"
	"bytes"
	"kubocd/internal/testarchive"
	"strings"
	"testing"
)

func TestListTarGzContentsRequiresCompleteArchive(t *testing.T) {
	raw := testarchive.Raw(t, testarchive.Entry{Name: "a", Data: "x"})
	corruptHeader := append([]byte(nil), raw...)
	corruptHeader[1024] = 'x'
	gzipCRC := testarchive.Gzip(t, raw)
	gzipCRC[len(gzipCRC)-8] ^= 1
	footer := testarchive.Gzip(t, raw)
	for name, data := range map[string][]byte{
		"invalid header":        testarchive.Gzip(t, corruptHeader),
		"truncated header":      testarchive.Gzip(t, raw[:200]),
		"truncated body":        testarchive.Gzip(t, raw[:512]),
		"missing trailer":       testarchive.Gzip(t, raw[:1024]),
		"one zero block":        testarchive.Gzip(t, raw[:1536]),
		"bad CRC":               gzipCRC,
		"truncated footer":      footer[:len(footer)-4],
		"corrupt second member": append(append([]byte(nil), footer...), 0x1f, 0x8b),
		"nonzero tail":          testarchive.Gzip(t, append(append([]byte(nil), raw...), 'x')),
	} {
		t.Run(name, func(t *testing.T) {
			listing, err := ListTarGzContentsReader(bytes.NewReader(data))
			if err == nil || listing != "" {
				t.Fatalf("listing succeeded after archive error: %q, %v", listing, err)
			}
		})
	}
	for name, data := range map[string][]byte{
		"ordinary":            testarchive.Gzip(t, raw),
		"zero padding":        testarchive.Gzip(t, append(append([]byte(nil), raw...), make([]byte, 512)...)),
		"concatenated chunks": append(testarchive.Gzip(t, raw[:600]), testarchive.Gzip(t, raw[600:])...),
	} {
		t.Run(name, func(t *testing.T) {
			listing, err := ListTarGzContentsReader(bytes.NewReader(data))
			if err != nil || listing != "a\n" {
				t.Fatalf("healthy archive failed: %q, %v", listing, err)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		listing, err := ListTarGzContentsReader(bytes.NewReader(testarchive.Gzip(t, testarchive.Raw(t))))
		if err != nil || listing != "" {
			t.Fatalf("empty archive failed: %q, %v", listing, err)
		}
	})
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		t.Run(format.String(), func(t *testing.T) {
			name := strings.Repeat("x", 300)
			raw := testarchive.Raw(t, testarchive.Entry{Name: name, Format: format})
			listing, err := ListTarGzContentsReader(bytes.NewReader(testarchive.Gzip(t, raw)))
			if err != nil || listing != name+"\n" {
				t.Fatalf("long name failed: %q, %v", listing, err)
			}
		})
	}
}
