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
	"strings"
	"testing"
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
