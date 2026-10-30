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
	amplifierCountKey     = "count"
	seqFunctionName       = "seq"
	untilStepFunctionName = "untilStep"
	repeatFunctionName    = "repeat"
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

const (
	joinFunctionName   = "join"
	printfFunctionName = "printf"
	regexFunctionName  = "regexReplaceAll"
	formatModelKey     = "format"
	regexPatternKey    = "pattern"
)

// checkConservativeTemplate checks the result and the refusal of a bounded function in every function set.
func checkConservativeTemplate(t *testing.T, name, call string, model map[string]interface{}, want string, refused bool) {
	t.Helper()
	forms := []struct{ text, want string }{
		{"{{ " + call + " | len }}", strconv.Itoa(len(want))},
		{"{{ $unused := " + call + " }}ok", "ok"},
	}
	if !refused {
		forms = append(forms, struct{ text, want string }{"{{ " + call + " }}", want})
	}
	for set, functions := range amplifierFunctionSets {
		t.Run(set, func(t *testing.T) {
			for i, form := range forms {
				t.Run(strconv.Itoa(i), func(t *testing.T) {
					tt, err := New("", form.text, "", functions)
					if err != nil {
						t.Fatal(err)
					}
					got, err := tt.RenderToText(model)
					if !refused {
						if err != nil || got != form.want {
							t.Fatalf("%s: got %d bytes (%v), want %d bytes equal to the original function", name, len(got), err, len(form.want))
						}
						return
					}
					if err == nil {
						if len(want) > maxRenderedText {
							t.Fatalf("%s accepted an intermediate larger than 4194304 bytes", name)
						}
						t.Fatalf("%s accepted a result whose size bound exceeds 4194304 bytes", name)
					}
					message := fmt.Sprintf("line 1, column %d: error calling %s: %s: the result can exceed 4194304 bytes",
						strings.Index(form.text, name)+1, name, name)
					if err.Error() != message || !errors.Is(err, ErrLimit) {
						t.Fatalf("got %v, want %q and ErrLimit", err, message)
					}
				})
			}
		})
	}
}

// TestTemplateJoinLimit covers exact string bounds and conservative stringification bounds.
func TestTemplateJoinLimit(t *testing.T) {
	original := sprig.TxtFuncMap()[joinFunctionName].(func(string, interface{}) string)
	check := func(t *testing.T, separator string, items interface{}, refused bool) {
		t.Helper()
		checkConservativeTemplate(t, joinFunctionName, "join .sep .items",
			map[string]interface{}{"sep": separator, "items": items}, original(separator, items), refused)
	}
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("items/%d", extra), func(t *testing.T) {
			check(t, "", []string{strings.Repeat("a", maxRenderedText/2), strings.Repeat("b", maxRenderedText/2+extra)}, extra > 0)
		})
		t.Run(fmt.Sprintf("separator/%d", extra), func(t *testing.T) {
			check(t, strings.Repeat("s", maxRenderedText+extra), []string{"", ""}, extra > 0)
		})
		t.Run(fmt.Sprintf("repeated separator/%d", extra), func(t *testing.T) {
			check(t, strings.Repeat("s", maxRenderedText/2-1), []string{"", "", strings.Repeat("a", 2+extra)}, extra > 0)
		})
	}
	for label, items := range map[string]interface{}{
		"nil": nil, "empty list": []string{}, "singleton": []string{"x"}, "nil elements": []interface{}{nil, "x", nil, "y"},
		"mixed": []interface{}{1, true, "é", nil}, "nested": []interface{}{[]interface{}{nil, "x"}, map[string]interface{}{"key": 2}},
		"scalar": 12,
	} {
		t.Run(label, func(t *testing.T) { check(t, ",", items, false) })
	}
	t.Run("unused separator", func(t *testing.T) { check(t, strings.Repeat("s", maxRenderedText+1), []string{"x"}, false) })
	t.Run("non-string over-count", func(t *testing.T) {
		check(t, "", []interface{}{[]string{strings.Repeat("a", maxRenderedText/4)}}, true)
	})
}

// checkPrintfTemplate compares a template printf call with fmt without needing new implementation helpers.
func checkPrintfTemplate(t *testing.T, format string, args []interface{}, refused bool) {
	t.Helper()
	call := "printf .format"
	model := map[string]interface{}{formatModelKey: format}
	for i, arg := range args {
		key := fmt.Sprintf("arg%d", i)
		model[key] = arg
		call += " ." + key
	}
	checkConservativeTemplate(t, printfFunctionName, call, model, fmt.Sprintf(format, args...), refused)
}

// TestTemplatePrintfLimit covers formatting amplification without rejecting ordinary large strings.
func TestTemplatePrintfLimit(t *testing.T) {
	for _, extra := range []int{-1, 0, 1} {
		for _, verb := range []string{"%s", "%v"} {
			t.Run(fmt.Sprintf("plain/%s/%d", verb, extra), func(t *testing.T) {
				checkPrintfTemplate(t, verb, []interface{}{strings.Repeat("a", maxRenderedText+extra)}, extra > 0)
			})
		}
		t.Run(fmt.Sprintf("two strings/%d", extra), func(t *testing.T) {
			checkPrintfTemplate(t, "%s%s", []interface{}{strings.Repeat("a", maxRenderedText/2), strings.Repeat("b", maxRenderedText/2+extra)}, extra > 0)
		})
	}
	for label, c := range map[string]struct {
		format  string
		args    []interface{}
		refused bool
	}{
		"nil type ignores precision":   {"%s%.0T", []interface{}{strings.Repeat("a", maxRenderedText), nil}, true},
		"nil type at limit":            {"%s%.0T", []interface{}{strings.Repeat("a", maxRenderedText-5), nil}, false},
		"boolean at limit":             {"%s%t", []interface{}{strings.Repeat("a", maxRenderedText-4), true}, false},
		"repeated boolean":             {strings.Repeat("%[1]t", 4097), []interface{}{true}, false},
		"padded boolean diagnostic":    {"%s%400d", []interface{}{strings.Repeat("a", maxRenderedText-410), false}, false},
		"nil diagnostic ignores width": {"%s%1000000s", []interface{}{strings.Repeat("a", maxRenderedText-10), nil}, false},
		"repeated binary integer":      {strings.Repeat("%[1]b", 4097), []interface{}{int64(-1 << 63)}, false},
		"quoted nil elements":          {"%#v", []interface{}{make([]interface{}, 250000)}, true},
		"five fields":                  {strings.Repeat("%1000000s", 5), []interface{}{"", "", "", "", ""}, true},
		"dynamic fields":               {strings.Repeat("%*s", 5), []interface{}{1000000, "", 1000000, "", 1000000, "", 1000000, "", 1000000, ""}, true},
		"indexed widths":               {strings.Repeat("%[1]*[2]s", 5), []interface{}{1000000, ""}, true},
		"repeated argument":            {strings.Repeat("%[1]s", 65), []interface{}{strings.Repeat("a", 64<<10)}, true},
		"long list":                    {"%10v", []interface{}{make([]string, 420000)}, true},
		"padded leaves":                {"%1000000v", []interface{}{[]string{"", "", "", "", ""}}, true},
		"literal width":                {"%4194305s", []interface{}{""}, true},
		"numeric precision":            {"%.4194305d", []interface{}{1}, true},
		"quoted over-count":            {"%q", []interface{}{strings.Repeat("a", maxRenderedText-2)}, true},
		"hex over-count":               {"%x", []interface{}{strings.Repeat("a", maxRenderedText/2)}, true},
		"large short precision":        {"%.9999999s", []interface{}{"short"}, false},
		"450 KiB string":               {"%s", []interface{}{strings.Repeat("a", 450<<10)}, false},
		"indexed unused argument":      {"%[1]s", []interface{}{"short", strings.Repeat("a", maxRenderedText+1)}, false},
		"extra argument":               {"", []interface{}{strings.Repeat("a", maxRenderedText+1)}, true},
		"type only":                    {"%T", []interface{}{strings.Repeat("a", maxRenderedText+1)}, false},
	} {
		t.Run(label, func(t *testing.T) { checkPrintfTemplate(t, c.format, c.args, c.refused) })
	}
	t.Run("complex literal", func(t *testing.T) {
		checkConservativeTemplate(t, printfFunctionName, `printf "%v" 1i`, nil, fmt.Sprintf("%v", 1i), false)
	})
	t.Run("pointer priority", func(t *testing.T) {
		_, err := printf("%4194305s%p", "", map[string]interface{}{})
		if err == nil || err.Error() != "printf: the %p verb is not allowed" || errors.Is(err, ErrLimit) {
			t.Fatalf("got %v, want the pointer prohibition before the size refusal", err)
		}
	})
}

// TestTemplateRegexReplaceAllLimit checks Go's match walk and the conservative replacement bound.
func TestTemplateRegexReplaceAllLimit(t *testing.T) {
	original := sprig.TxtFuncMap()[regexFunctionName].(func(string, string, string) string)
	check := func(t *testing.T, pattern, source, replacement string, refused bool) {
		t.Helper()
		checkConservativeTemplate(t, regexFunctionName, "regexReplaceAll .pattern .source .repl",
			map[string]interface{}{regexPatternKey: pattern, amplifierSourceKey: source, "repl": replacement}, original(pattern, source, replacement), refused)
	}
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("literal boundary/%d", extra), func(t *testing.T) {
			check(t, "x", "x", strings.Repeat("r", maxRenderedText+extra), extra > 0)
		})
		t.Run(fmt.Sprintf("capture bound/%d", extra), func(t *testing.T) {
			check(t, "(?s)^.*$", strings.Repeat("a", maxRenderedText-2+extra), "$0", extra > 0)
		})
	}
	for label, c := range map[string]struct {
		pattern, source, replacement string
		refused                      bool
	}{
		"oversized capture":      {"(?s)^(.*)$", strings.Repeat("a", maxRenderedText+1), "$1", true},
		"short group over-count": {"(?s)^(a).*$", strings.Repeat("a", maxRenderedText-1), "$1", true},
		"empty dollar escape":    {"", "", "$$", false},
		"empty Unicode matches":  {"", invalidUnicodeString, "$$", false},
		"many empty matches":     {"()()()()()()()()()()", strings.Repeat("a", 1<<16), "", false},
		"anchors":                {"(?m)^a|b$", "aab\naab", "-", false},
		"word boundaries":        {`\b(a)\b`, "a ba a", "${1}x", false},
		"capture syntax":         {"(?P<first>a)(b)?", "ab a", "${first}:$0:$1x:${1}x:$2:$missing:$$:${", false},
		"duplicate names":        {"(?P<part>a)|(?P<part>b)", "ab", "${part}", false},
		"empty after match":      {"a*", "aab", "-", false},
		"no matches":             {"z", shortFormatText, strings.Repeat("r", maxRenderedText+1), false},
		"shrinking source":       {"a+", strings.Repeat("a", maxRenderedText+1), "", false},
		"unchanged source":       {"z", strings.Repeat("a", maxRenderedText+1), "", true},
	} {
		t.Run(label, func(t *testing.T) { check(t, c.pattern, c.source, c.replacement, c.refused) })
	}
	t.Run("invalid pattern withheld", func(t *testing.T) {
		for set, functions := range amplifierFunctionSets {
			t.Run(set, func(t *testing.T) {
				tt, err := New("", "{{ regexReplaceAll .pattern .source .repl }}", "", functions)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tt.RenderToText(map[string]interface{}{regexPatternKey: "[pattern-canary", amplifierSourceKey: amplifierSourceCanary, "repl": "replacement-canary"})
				want := "line 1, column 4: error calling regexReplaceAll, " + withheld
				if err == nil || err.Error() != want || errors.Is(err, ErrLimit) {
					t.Fatalf("got %v, want %q", err, want)
				}
			})
		}
	})
}

func TestTemplateRepeatLimit(t *testing.T) {
	for _, extra := range []int{-1, 0, 1} {
		t.Run(fmt.Sprintf("boundary/%d", extra), func(t *testing.T) {
			count := maxRenderedText + extra
			checkTemplateAmplifier(t, repeatFunctionName, "repeat .count .source",
				map[string]interface{}{amplifierCountKey: count, amplifierSourceKey: "x"}, strings.Repeat("x", count), extra > 0)
		})
	}
	for name, c := range map[string]struct {
		count        int
		source, want string
		refused      bool
	}{
		"product overflow":   {math.MaxInt, "private", "", true},
		"empty input":        {math.MaxInt, "", "", false},
		"zero count":         {0, strings.Repeat("x", maxRenderedText+1), "", false},
		"Unicode at limit":   {maxRenderedText / 2, "é", strings.Repeat("é", maxRenderedText/2), false},
		"Unicode over limit": {maxRenderedText/2 + 1, "é", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			checkTemplateAmplifier(t, repeatFunctionName, "repeat .count .source",
				map[string]interface{}{amplifierCountKey: c.count, amplifierSourceKey: c.source}, c.want, c.refused)
		})
	}
	for _, count := range []int{-1, math.MinInt} {
		for _, source := range []string{"", "private"} {
			got, err := repeat(count, source)
			if got != "" || err == nil || err.Error() != "repeat: the count must not be negative" || !errors.Is(err, ErrLimit) {
				t.Fatalf("negative repeat: got %q (%v)", got, err)
			}
		}
	}
	tt, err := New("", `{{ repeat -1 "private" }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tt.RenderToText(nil)
	if want := "line 1, column 4: error calling repeat: repeat: the count must not be negative"; err == nil || err.Error() != want || !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v, want %q and ErrLimit", err, want)
	}
}

func TestUntilStepMatchesSprig(t *testing.T) {
	original := sprig.TxtFuncMap()[untilStepFunctionName].(func(int, int, int) []int)
	for start := -3; start <= 3; start++ {
		for stop := -3; stop <= 3; stop++ {
			for step := -4; step <= 4; step++ {
				got, err := untilStep(start, stop, step)
				if want := original(start, stop, step); err != nil || got == nil || !slices.Equal(got, want) {
					t.Fatalf("untilStep(%d, %d, %d): got %v (%v), want %v", start, stop, step, got, err, want)
				}
			}
		}
	}
}

func TestUntilStepBoundsAndOverflow(t *testing.T) {
	for name, c := range map[string]struct {
		start, stop, step, count, last int
	}{
		"ascending limit":      {0, maxUntil, 1, maxUntil, maxUntil - 1},
		"descending limit":     {0, -maxUntil, -1, maxUntil, -maxUntil + 1},
		"stride limit":         {0, 2 * maxUntil, 2, maxUntil, 2*maxUntil - 2},
		"upper edge":           {math.MaxInt - 1, math.MaxInt, 2, 1, math.MaxInt - 1},
		"lower edge":           {math.MinInt + 1, math.MinInt, -2, 1, math.MinInt + 1},
		"full distance":        {math.MinInt, math.MaxInt, math.MaxInt, 3, math.MaxInt - 1},
		"minimum step":         {math.MaxInt, math.MinInt, math.MinInt, 2, -1},
		"minimum step at zero": {0, math.MinInt, math.MinInt, 1, 0},
		"wrong direction":      {math.MinInt, math.MaxInt, math.MinInt, 0, 0},
		"zero step":            {math.MinInt, math.MaxInt, 0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := untilStep(c.start, c.stop, c.step)
			if err != nil || len(got) != c.count {
				t.Fatalf("got %d items (%v), want %d", len(got), err, c.count)
			}
			if c.count > 0 && (got[0] != c.start || got[len(got)-1] != c.last) {
				t.Fatalf("got endpoints %d, %d, want %d, %d", got[0], got[len(got)-1], c.start, c.last)
			}
		})
	}
	for _, params := range [][3]int{{0, maxUntil + 1, 1}, {0, -maxUntil - 1, -1}, {0, 2*maxUntil + 1, 2},
		{math.MinInt, math.MaxInt, 1}, {math.MaxInt, math.MinInt, -1}} {
		got, err := untilStep(params[0], params[1], params[2])
		if len(got) != 0 || err == nil || err.Error() != "untilStep: the count is beyond the limit of 10000" || !errors.Is(err, ErrLimit) {
			t.Fatalf("untilStep %v: got %d items (%v), want a count limit error", params, len(got), err)
		}
	}
}

func TestSeqMatchesSprig(t *testing.T) {
	original := sprig.TxtFuncMap()[seqFunctionName].(func(...int) string)
	check := func(params ...int) {
		t.Helper()
		got, err := seq(params...)
		if want := original(params...); err != nil || got != want {
			t.Fatalf("seq %v: got %q (%v), want %q", params, got, err, want)
		}
	}
	check()
	check(1, 2, 3, 4)
	for start := -3; start <= 3; start++ {
		check(start)
		for stop := -3; stop <= 3; stop++ {
			check(start, stop)
			for step := -4; step <= 4; step++ {
				check(start, step, stop)
			}
		}
	}
}

func TestSeqBoundsAndOverflow(t *testing.T) {
	for _, params := range [][]int{{maxUntil}, {2 - maxUntil}, {1, maxUntil}, {0, -maxUntil + 1},
		{0, 2, 2 * (maxUntil - 1)}, {math.MaxInt - maxUntil + 1, math.MaxInt}, {math.MinInt + maxUntil - 1, math.MinInt}} {
		got, err := seq(params...)
		if err != nil || len(strings.Fields(got)) != maxUntil {
			t.Fatalf("seq %v: got %d bytes (%v), want 10000 numbers", params, len(got), err)
		}
		if len(got) >= maxRenderedText/10 {
			t.Fatalf("10000 numbers, including integer extremes, must stay below one tenth of maxRenderedText, got %d bytes", len(got))
		}
	}
	for _, params := range [][]int{{maxUntil + 1}, {1 - maxUntil}, {1, maxUntil + 1}, {0, -maxUntil},
		{0, 2, 2 * maxUntil}, {math.MaxInt}, {math.MinInt}, {math.MinInt, math.MaxInt}, {math.MaxInt, math.MinInt},
		{math.MinInt, 1, math.MaxInt}, {math.MaxInt, -1, math.MinInt}} {
		got, err := seq(params...)
		if got != "" || err == nil || err.Error() != "seq: the count is beyond the limit of 10000" || !errors.Is(err, ErrLimit) {
			t.Fatalf("seq %v: got %d bytes (%v), want a count limit error", params, len(got), err)
		}
	}
	for _, c := range []struct {
		params []int
		want   string
	}{
		{[]int{math.MaxInt, math.MaxInt}, strconv.Itoa(math.MaxInt)},
		{[]int{math.MinInt, math.MinInt}, strconv.Itoa(math.MinInt)},
		{[]int{math.MinInt, math.MaxInt, math.MaxInt}, fmt.Sprintf("%d -1 %d", math.MinInt, math.MaxInt-1)},
		{[]int{math.MaxInt, math.MinInt, math.MinInt}, fmt.Sprintf("%d -1", math.MaxInt)},
		{[]int{0, math.MinInt, math.MinInt}, "0 " + strconv.Itoa(math.MinInt)},
		{[]int{math.MinInt, math.MinInt, math.MaxInt}, ""},
		{[]int{math.MinInt, 0, math.MaxInt}, ""},
	} {
		got, err := seq(c.params...)
		if err != nil || got != c.want {
			t.Fatalf("seq %v: got %q (%v), want %q", c.params, got, err, c.want)
		}
	}
}

func TestTemplateSequenceLimits(t *testing.T) {
	for name, call := range map[string]string{untilStepFunctionName: "untilStep 0 .count 1", seqFunctionName: "seq .count"} {
		for _, extra := range []int{0, 1} {
			for _, functions := range amplifierFunctionSets {
				tt, err := New("", "{{ $unused := "+call+" }}ok", "", functions)
				if err != nil {
					t.Fatal(err)
				}
				got, err := tt.RenderToText(map[string]interface{}{amplifierCountKey: maxUntil + extra})
				if extra == 0 {
					if err != nil || got != "ok" {
						t.Fatalf("%s at limit: got %q (%v)", name, got, err)
					}
				} else {
					want := "line 1, column 15: error calling " + name + ": " + name + ": the count is beyond the limit of 10000"
					if err == nil || err.Error() != want || !errors.Is(err, ErrLimit) {
						t.Fatalf("%s over limit: got %v, want %q and ErrLimit", name, err, want)
					}
				}
			}
		}
	}
}
