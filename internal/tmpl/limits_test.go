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
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Masterminds/sprig/v3"
)

const renderedTextRefusal = "the rendered text is larger than 4194304 bytes"

func TestATemplateRendersUpToTheLimitAndNoMore(t *testing.T) {
	quarter := maxRenderedText / 4
	cases := map[string]func(extra int) (text string, model map[string]interface{}){
		"literal text": func(extra int) (string, map[string]interface{}) {
			return strings.Repeat("a", maxRenderedText+extra), nil
		},
		"action output": func(extra int) (string, map[string]interface{}) {
			return "{{ .s }}", map[string]interface{}{"s": strings.Repeat("a", maxRenderedText+extra)}
		},
		"writes added up": func(extra int) (string, map[string]interface{}) {
			return "{{ range 4 }}{{ $.s }}{{ end }}{{ $.e }}", map[string]interface{}{"s": strings.Repeat("a", quarter),
				"e": strings.Repeat("a", extra)}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			text, model := build(0)
			tt, err := New("", text, "", ReleaseFunctions())
			if err != nil {
				t.Fatal(err)
			}
			got, err := tt.RenderToText(model)
			if err != nil || len(got) != maxRenderedText {
				t.Fatalf("got %d bytes and the error %v, want %d bytes", len(got), err, maxRenderedText)
			}
			text, model = build(1)
			if tt, err = New("", text, "", ReleaseFunctions()); err != nil {
				t.Fatal(err)
			}
			_, err = tt.RenderToText(model)
			if err == nil || err.Error() != renderedTextRefusal || !errors.Is(err, ErrLimit) {
				t.Fatalf("got the error %v, want %q, a limit error", err, renderedTextRefusal)
			}
		})
	}
}

func TestAMapRendersUpToTheLimitAndNoMore(t *testing.T) {
	for extra, refused := range map[int]bool{0: false, 1: true} {
		tt, err := New("", "key: {{ .s }}", "", ReleaseFunctions())
		if err != nil {
			t.Fatal(err)
		}
		m, err := tt.RenderToMap(map[string]interface{}{"s": strings.Repeat("a", maxRenderedText-len("key: ")+extra)})
		if refused {
			if err == nil || err.Error() != renderedTextRefusal || !errors.Is(err, ErrLimit) {
				t.Fatalf("got the error %v, want %q, a limit error", err, renderedTextRefusal)
			}
			continue
		}
		if err != nil || len(m["key"].(string)) != maxRenderedText-len("key: ") {
			t.Fatalf("got the error %v, want the map of a text of the limit", err)
		}
	}
}

func TestARenderStopsAtTheLimit(t *testing.T) {
	const piece = 1024
	calls := 0
	functions := Functions{funcs: withFunction(releaseFunctions.funcs, "piece", func() string {
		calls++
		return strings.Repeat("a", piece)
	})}
	tt, err := New("", "{{ range 10000 }}{{ piece }}{{ end }}", "", functions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tt.RenderToText(nil); err == nil || err.Error() != renderedTextRefusal {
		t.Fatalf("got the error %v, want %q", err, renderedTextRefusal)
	}
	if calls != maxRenderedText/piece+1 {
		t.Fatalf("got %d calls, want the render to stop at the write past the limit, call %d", calls, maxRenderedText/piece+1)
	}
}

func TestUntilBuildsUpToTheLimitEitherWay(t *testing.T) {
	for _, count := range []int{3, -3, 0, maxUntil, -maxUntil} {
		got, err := until(count)
		if want := sprigUntil(count); err != nil || !slices.Equal(got, want) {
			t.Errorf("until %d: got %d items and the error %v, want the %d items of sprig", count, len(got), err, len(want))
		}
	}
	const refusal = "line 1, column 4: error calling until: until: the count is beyond the limit of 10000"
	for _, count := range []string{"10001", "-10001"} {
		tt, err := New("", "{{ until "+count+" | len }}", "", ReleaseFunctions())
		if err != nil {
			t.Fatal(err)
		}
		_, err = tt.RenderToText(nil)
		if err == nil || err.Error() != refusal || !errors.Is(err, ErrLimit) {
			t.Errorf("until %s: got the error %v, want %q, a limit error", count, err, refusal)
		}
	}
}

const (
	amplifierWidthKey     = "width"
	nindentFunctionName   = "nindent"
	amplifierSourceKey    = "source"
	amplifierSourceCanary = "source-canary"
	indentFunctionName    = "indent"
)

var amplifierFunctionSets = map[string]Functions{
	"release": ReleaseFunctions(), "package": PackageFunctions(false), "package with DNS": PackageFunctions(true),
}

func checkTemplateAmplifier(t *testing.T, name, call string, model map[string]interface{}, want string, refused bool) {
	t.Helper()
	forms := []struct{ name, text, want string }{
		{"length", "{{ " + call + " | len }}", strconv.Itoa(len(want))},
		{"discarded", "{{ $unused := " + call + " }}ok", "ok"},
	}
	if !refused {
		forms = append(forms, struct{ name, text, want string }{"value", "{{ " + call + " }}", want})
	}
	for set, functions := range amplifierFunctionSets {
		t.Run(set, func(t *testing.T) {
			for _, form := range forms {
				t.Run(form.name, func(t *testing.T) {
					tt, err := New("", form.text, "", functions)
					if err != nil {
						t.Fatal(err)
					}
					got, err := tt.RenderToText(model)
					if refused {
						if err == nil {
							t.Fatalf("%s accepted an intermediate larger than 4194304 bytes", name)
						}
						message := fmt.Sprintf("line 1, column %d: error calling %s: %s: the result is larger than 4194304 bytes",
							strings.Index(form.text, name)+1, name, name)
						if err.Error() != message || !errors.Is(err, ErrLimit) {
							t.Fatalf("%s refusal is not the expected limit error: got %v, want %q", name, err, message)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if got != form.want {
						t.Fatalf("%s result differs from Sprig: got %d bytes, want %d bytes", name, len(got), len(form.want))
					}
				})
			}
		})
	}
}

func checkTemplateIndentation(t *testing.T, name string, leadingNewline int) {
	t.Helper()
	original := sprig.TxtFuncMap()[name].(func(int, string) string)
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("boundary/%d", extra), func(t *testing.T) {
			width := maxRenderedText - leadingNewline - 1 + extra
			model := map[string]interface{}{amplifierWidthKey: width, amplifierSourceKey: "x"}
			checkTemplateAmplifier(t, name, name+" .width .source", model, original(width, "x"), extra > 0)
		})
	}
	for label, c := range map[string]struct {
		width  int
		source string
	}{
		"zero width":       {0, "a\nb\n"},
		"empty input":      {3, ""},
		"empty zero width": {0, ""},
		"trailing newline": {2, "a\n"},
		"several lines":    {3, "a\nb\n\nc"},
		"Unicode":          {2, "é\n界\n"},
		"line product":     {maxRenderedText / 2, "\n"},
		"source too large": {0, strings.Repeat("a", maxRenderedText+1)},
		"source canary":    {maxRenderedText / 2, amplifierSourceCanary + "\n"},
	} {
		t.Run(label, func(t *testing.T) {
			want := original(c.width, c.source)
			checkTemplateAmplifier(t, name, name+" .width .source",
				map[string]interface{}{amplifierWidthKey: c.width, amplifierSourceKey: c.source}, want, len(want) > maxRenderedText)
		})
	}
}

func TestTemplateIndentLimit(t *testing.T) {
	checkTemplateIndentation(t, indentFunctionName, 0)
}

func TestTemplateNindentLimit(t *testing.T) {
	checkTemplateIndentation(t, nindentFunctionName, 1)
	t.Run("indent fits but nindent does not", func(t *testing.T) {
		model := map[string]interface{}{amplifierWidthKey: maxRenderedText, amplifierSourceKey: ""}
		checkTemplateAmplifier(t, indentFunctionName, "indent .width .source", model, strings.Repeat(" ", maxRenderedText), false)
		checkTemplateAmplifier(t, nindentFunctionName, "nindent .width .source", model, "", true)
	})
}

func TestTemplateReplaceLimit(t *testing.T) {
	original := sprig.TxtFuncMap()["replace"].(func(string, string, string) string)
	check := func(t *testing.T, old, replacement, source string) {
		t.Helper()
		want := original(old, replacement, source)
		checkTemplateAmplifier(t, "replace", "replace .old .replacement .source",
			map[string]interface{}{"old": old, "replacement": replacement, amplifierSourceKey: source}, want, len(want) > maxRenderedText)
	}
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("nonempty search boundary/%d", extra), func(t *testing.T) {
			check(t, "x", strings.Repeat("r", (maxRenderedText+extra)/2), "xx"+strings.Repeat("!", (maxRenderedText+extra)%2))
		})
		t.Run(fmt.Sprintf("empty search boundary/%d", extra), func(t *testing.T) {
			source := "x"
			if (maxRenderedText+extra)%2 == 0 {
				source = "é"
			}
			check(t, "", strings.Repeat("r", (maxRenderedText+extra-len(source))/2), source)
		})
	}
	for label, c := range map[string]struct{ old, replacement, source string }{
		"overlapping candidates":     {"aa", "b", "aaaaa"},
		"no matches":                 {"z", "replacement-canary", amplifierSourceCanary},
		"unused large replacement":   {"z", strings.Repeat("r", maxRenderedText+1), "a"},
		"deletion":                   {"a", "", "abracadabra"},
		"empty replacement and old":  {"", "", "é界"},
		"empty source":               {"a", "x", ""},
		"empty source and old":       {"", "é", ""},
		"Unicode boundaries":         {"", "!", "é界"},
		"invalid UTF-8 boundaries":   {"", "!", "\xffé\xfe"},
		"Unicode search":             {"é", "界", "ééx"},
		"shrinking oversized source": {"a", "", strings.Repeat("a", maxRenderedText+1)},
		"oversized unchanged source": {"z", "", strings.Repeat("a", maxRenderedText+1)},
		"canaries":                   {amplifierSourceCanary, strings.Repeat("replacement-canary", maxRenderedText/18+1), amplifierSourceCanary},
	} {
		t.Run(label, func(t *testing.T) { check(t, c.old, c.replacement, c.source) })
	}
}

func TestTemplateIndentationRejectsNegative(t *testing.T) {
	for _, name := range []string{indentFunctionName, nindentFunctionName} {
		for set, functions := range amplifierFunctionSets {
			t.Run(name+"/"+set, func(t *testing.T) {
				tt, err := New("", "{{ "+name+" -1 .source }}", "", functions)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tt.RenderToText(map[string]interface{}{amplifierSourceKey: amplifierSourceCanary})
				want := fmt.Sprintf("line 1, column 4: error calling %s: %s: the indentation must not be negative", name, name)
				if err == nil || err.Error() != want || !errors.Is(err, ErrLimit) {
					t.Fatalf("%s refusal is not the expected limit error: got %v, want %q", name, err, want)
				}
			})
		}
	}
}

func TestTemplateAmplifierOverflow(t *testing.T) {
	for _, name := range []string{indentFunctionName, nindentFunctionName} {
		t.Run(name, func(t *testing.T) {
			checkTemplateAmplifier(t, name, name+" .width .source",
				map[string]interface{}{amplifierWidthKey: int(^uint(0) >> 1), amplifierSourceKey: "\n"}, "", true)
		})
	}
}
