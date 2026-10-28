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

package tmpl

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

const (
	shortFormatText      = "text"
	invalidUnicodeString = "é\xff界"
	dynamicStringWidth   = "%*s"
)

var rejectedFormats = []string{
	"%p", "%[1]p", "%-10p", "%#p", "%.3p", "%.p", "%*p", "%[2]*[1]p", "%%%p", "a %s b %p",
	"%[p", "%[x]p", "%[1p", "%+ #0-p",
}

var acceptedFormats = []string{
	"", "100%", "%%p", "%s", "%v", "%d", "%5.2f", "%-8s|", "%[2]s %[1]s", "%x", "%T", "%#v", "%q",
	"%5.", "%!p", "%9999999999p", "%ép", "%[1]*s", "%.*s", "%w",
}

func TestPrintfRejectsThePointerVerb(t *testing.T) {
	for _, format := range rejectedFormats {
		if _, err := printf(format, map[string]interface{}{"a": 1}); err == nil || !hasPointerVerb(format) {
			t.Errorf("%q: the %%p verb should be rejected", format)
		}
	}
	for _, format := range acceptedFormats {
		args := []interface{}{"x", 1.5}
		got, err := printf(format, args...)
		if err != nil || hasPointerVerb(format) {
			t.Errorf("%q: unexpected error %v", format, err)
			continue
		}
		if want := fmt.Sprintf(format, args...); got != want {
			t.Errorf("%q: got %q, want %q", format, got, want)
		}
	}
}

func TestPrintfInATemplate(t *testing.T) {
	tt, err := New("", `{{ printf "%s-%03d" .name 7 }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tt.RenderToText(map[string]interface{}{"name": "db"}); err != nil || got != "db-007" {
		t.Fatalf("got %q (%v), want %q", got, err, "db-007")
	}
	tt, err = New("", `{{ printf .format .data }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tt.RenderToText(map[string]interface{}{"format": "%p", "data": map[string]interface{}{}})
	if want := "printf: the %p verb is not allowed"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

// FuzzPrintfGuard checks that printf behaves as fmt.Sprintf whenever it
// accepts a format, and that it rejects every format that makes fmt.Sprintf
// print an address or a misplaced %p.
func FuzzPrintfGuard(f *testing.F) {
	for _, format := range append(rejectedFormats, acceptedFormats...) {
		f.Add(format)
	}
	args := []interface{}{
		map[string]interface{}{"a": 1}, map[string]interface{}{"b": 2}, []interface{}{"c"}, map[string]interface{}{},
	}
	addresses := make([]string, 0, len(args))
	for _, arg := range args {
		addresses = append(addresses, strings.TrimPrefix(fmt.Sprintf("%p", arg), "0x"))
	}
	printsAddress := func(s string) bool {
		for _, address := range addresses {
			if strings.Contains(s, address) {
				return true
			}
		}
		return false
	}
	f.Fuzz(func(t *testing.T, format string) {
		want := fmt.Sprintf(format, args...)
		got, err := printf(format, args...)
		if err == nil {
			if got != want {
				t.Fatalf("%q: got %q, want %q", format, got, want)
			}
			if printsAddress(got) {
				t.Fatalf("%q: accepted, but prints an address: %q", format, got)
			}
			return
		}
		if errors.Is(err, ErrLimit) {
			return
		}
		if !printsAddress(want) && !strings.Contains(want, "%!p(") {
			t.Fatalf("%q: rejected, but fmt.Sprintf prints no pointer verb: %q", format, want)
		}
	})
}

// TestPrintfFormattingCompatibility keeps fmt as the producer for ordinary and malformed formats.
func TestPrintfFormattingCompatibility(t *testing.T) {
	cases := []struct {
		format string
		args   []interface{}
	}{
		{"%.0T %.0v %100s", []interface{}{nil, nil, nil}},
		{"%.0t %#v %20d", []interface{}{true, false, false}},
		{"%v %v", []interface{}{complex64(1i), complex128(1i)}},
		{"%#.10f %20.4x %q", []interface{}{complex(math.MaxFloat64, -math.MaxFloat64), complex64(1 + 2i), complex128(1 - 2i)}},
		{"%s-main", []interface{}{"demo"}}, {"%s.%s", []interface{}{"a", "b"}}, {"%s-%03d", []interface{}{"a", 7}},
		{"%v %+v %#v", []interface{}{[]interface{}{nil, "é"}, map[string]interface{}{"x": 1}, map[string]interface{}{"x": "é"}}},
		{"%T %t", []interface{}{map[string]interface{}{}, true}},
		{"%d %+08d %#x", []interface{}{int64(math.MinInt64), 42, uint64(math.MaxUint64)}},
		{"%x % X %# x", []interface{}{"é", "ab", "ab"}},
		{"%q %+q %#q", []interface{}{"é\xff", "é\xff", "plain"}},
		{"%-8s|%.2s", []interface{}{"界", invalidUnicodeString}},
		{"%5.2f %g %#g %e %x", []interface{}{1.5, math.MaxFloat64, 1.5, 1.5, 1.5}},
		{"%% %9999999%", nil}, {"%[2]s %[1]s", []interface{}{"a", "b"}},
		{dynamicStringWidth, []interface{}{-8, "é"}}, {"%.*s", []interface{}{2, invalidUnicodeString}},
		{"%*.*f", []interface{}{8, 2, 1.5}}, {"%[3]*.[2]*[1]f", []interface{}{1.5, 2, 8}},
		{"%9999999999s", []interface{}{"x"}}, {"%1000001s", []interface{}{"x"}},
		{dynamicStringWidth, []interface{}{int(^uint(0) >> 1), "x"}}, {dynamicStringWidth, []interface{}{-int(^uint(0)>>1) - 1, "x"}},
		{"%.*s", []interface{}{-2, shortFormatText}}, {"%.*s", []interface{}{1000001, shortFormatText}},
		{dynamicStringWidth, []interface{}{"bad width", "x"}}, {"%.*s", []interface{}{"bad precision", "x"}},
		{"%[9]s", []interface{}{"x"}}, {"%s", nil}, {"%s", []interface{}{"x", "extra"}},
		{"%d", []interface{}{"wrong verb"}}, {"%w", []interface{}{"wrong verb"}},
		{"%*.*s", []interface{}{4, 2, "é界x"}}, {"%[1]s", []interface{}{"x", "unused"}},
		{"%.9999999t", []interface{}{true}}, {"%.9999999v", []interface{}{nil}},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%d/%s", i, c.format), func(t *testing.T) {
			got, err := printf(c.format, c.args...)
			want := fmt.Sprintf(c.format, c.args...)
			if err != nil || got != want {
				t.Fatalf("got %d bytes (%v), want %d bytes equal to fmt", len(got), err, len(want))
			}
		})
	}
}
