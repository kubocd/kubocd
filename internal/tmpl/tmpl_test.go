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

//nolint:goconst // test inputs read better inline
package tmpl

import (
	"errors"
	"strings"
	"testing"
)

const canary = "s3cr3t-SEC1-canary"

func TestRenderErrorsDoNotQuoteTheData(t *testing.T) {
	type output struct {
		Name string `json:"name"`
	}
	cases := map[string]struct {
		text   string
		render func(Tmpl, map[string]interface{}) error
		want   string
	}{
		"map, invalid YAML": {"password: {{ .secret }}\nbroken: [unclosed", renderMap, "the rendered text is not valid YAML (line 2)"},
		"map, not a map":    {"- {{ .secret }}", renderMap, "the rendered text is not a YAML map"},
		"map, bad tag":      {"value: !!int {{ .secret }}", renderMap, "the rendered text is not valid YAML"},
		"map, complex key":  {"? [{{ .secret }}]\n: x", renderMap, "the rendered text is not valid YAML"},
		"list of strings":   {"- a\n- {{ .secret }}: x", renderStringList, "the rendered text is not a YAML list of strings"},
		"boolean":           {"{{ .secret }}", renderBool, "the rendered text is not a boolean"},
		"duration":          {"{{ .secret }}", renderDuration, "the rendered text is not a duration"},
		"unknown field": {"- name: a\n  {{ .secret }}: x", func(tt Tmpl, model map[string]interface{}) error {
			return tt.RenderInto(model, &[]*output{})
		}, "the rendered text does not have the expected structure"},
		"urlParse":      {`{{ urlParse (printf "postgres://user:%s@host:port/db" .secret) }}`, renderText, "urlParse: invalid URL"},
		"imageRedirect": {`{{ imageRedirect (printf "a:%s:b:c" .secret) . }}`, renderText, "imageRedirect: the image reference is invalid"},
	}
	model := map[string]interface{}{"secret": canary, ImageRedirectsKey: ImageRedirectTable(nil)}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, "", PackageFunctions(false))
			if err != nil {
				t.Fatal(err)
			}
			err = c.render(tt, model)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
			if strings.Contains(err.Error(), canary[:7]) {
				t.Fatalf("the error quotes the data: %v", err)
			}
		})
	}
}

func TestAuthorMessagesAreKeptAsWritten(t *testing.T) {
	tt, err := New("", `{{ fail (printf "the password %s is too short" .secret) }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tt.RenderToText(map[string]interface{}{"secret": canary})
	if want := "error calling fail: the password " + canary + " is too short"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

func TestParseErrorInTheHeaderNamesTheHeader(t *testing.T) {
	const either = "in templateHeader or on the first line of the template: "
	cases := map[string]struct {
		header string
		text   string
		place  string
	}{
		"header with a final newline":           {"{{- $host := .Context.host }}\n{{- $now := now }}\n", "host: {{ $host }}", "in templateHeader: "},
		"one line header":                       {"{{- $now := now }}", "host: x", either},
		"block header, error on its last line":  {"{{- $host := .Context.host }}\n{{- $now := now }}", "host: {{ $host }}", either},
		"block header, error on its first line": {"{{- $now := now }}\n{{- $host := .Context.host }}", "host: {{ $host }}", "in templateHeader: "},
		"error in the template":                 {"{{- $host := .Context.host }}\n", "host: {{ now }}", ""},
		"error after a block header":            {"{{- $host := .Context.host }}", "\nhost: {{ now }}", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New("", c.text, c.header, PackageFunctions(false))
			if err == nil || !strings.Contains(err.Error(), `function "now" not defined`) {
				t.Fatalf("got error %v, want a parse error on now", err)
			}
			if c.place == "" && strings.Contains(err.Error(), "templateHeader") {
				t.Fatalf("got error %v, want one that does not blame the templateHeader", err)
			}
			if c.place != "" && !strings.HasPrefix(err.Error(), c.place) {
				t.Fatalf("got error %v, want one starting with %q", err, c.place)
			}
		})
	}
}

func TestExecutionErrorsDoNotQuoteTheData(t *testing.T) {
	functions := Functions{funcs: withFunction(ReleaseFunctions().funcs, "explode", func(s string) string { panic("exploded on " + s) })}
	cases := map[string]struct {
		text string
		want string
	}{
		"range over a string":        {"a: 1\nb: {{ range .secret }}{{ end }}", "line 2, column 13: range over a value that is not a list or a map, " + withheld},
		"comparison of maps":         {"{{ if eq .other .db }}x{{ end }}", "line 1, column 7: error calling eq, " + withheld},
		"invalid pattern":            {`{{ regexReplaceAll .pattern "x" "y" }}`, "line 1, column 4: error calling regexReplaceAll, " + withheld},
		"recovered panic":            {"{{ explode .secret }}", "line 1, column 4: error calling explode, " + withheld},
		"index out of range":         {"{{ index .list 5 }}", "line 1, column 4: error calling index, " + withheld},
		"argument of the wrong type": {"{{ upper .db }}", "line 1, column 10: argument of the wrong type, " + withheld},
		"field of a string":          {"{{ .secret.port }}", "line 1, column 11: field of a value that is not a map, " + withheld},
	}
	model := map[string]interface{}{
		"secret":  canary,
		"pattern": canary + "(",
		"db":      map[string]interface{}{"password": canary},
		"other":   map[string]interface{}{"password": "other"},
		"list":    []interface{}{canary},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, "", functions)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tt.RenderToText(model)
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
			if errors.Unwrap(err) != nil {
				t.Fatalf("the error wraps the message of text/template: %v", errors.Unwrap(err))
			}
		})
	}
}

func TestKuboCDMessagesAreKept(t *testing.T) {
	cases := map[string]struct {
		text string
		want string
	}{
		"fail":       {`{{ fail "the bucket is mandatory" }}`, "line 1, column 4: error calling fail: the bucket is mandatory"},
		"required":   {`x: {{ required "a host is required" .host }}`, "line 1, column 7: error calling required: a host is required"},
		"printf":     {`{{ printf "%p" .list }}`, "line 1, column 4: error calling printf: printf: the %p verb is not allowed"},
		"resolveDNS": {`{{ resolveDNS "localhost" }}`, "line 1, column 4: error calling resolveDNS: resolveDNS is disabled by platform policy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, "", PackageFunctions(false))
			if err != nil {
				t.Fatal(err)
			}
			_, err = tt.RenderToText(map[string]interface{}{"list": []interface{}{1}})
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
		})
	}
}

func TestExecutionErrorInTheHeaderNamesTheHeader(t *testing.T) {
	const required = `{{- $host := required "a host is required" .host }}`
	cases := map[string]struct {
		header string
		text   string
		want   string
	}{
		"header with a final newline": {required + "\n", "host: {{ $host }}", "in templateHeader, line 1, column 14: error calling required: a host is required"},
		"one line header":             {required, "host: {{ $host }}", "in templateHeader, line 1, column 14: error calling required: a host is required"},
		"error in the template":       {"{{- $port := 80 }}", `host: {{ required "a host is required" .host }}`, "line 1, column 10: error calling required: a host is required"},
		"error after a block header":  {"{{- $port := 80 }}\n{{- $x := 1 }}", "\nhost: {{ required \"a host is required\" .host }}", "line 2, column 10: error calling required: a host is required"},
		"error after a header with a final newline": {"{{- $port := 80 }}\n{{- $x := 1 }}\n", "a: 1\nhost: {{ required \"a host is required\" .host }}",
			"line 2, column 10: error calling required: a host is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, c.header, PackageFunctions(false))
			if err != nil {
				t.Fatal(err)
			}
			_, err = tt.RenderToText(map[string]interface{}{})
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
		})
	}
}

func TestYAMLLineComesFromTheParserOnly(t *testing.T) {
	tt, err := New("", `value: !!int "{{ .fake }}"`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = tt.RenderToMap(map[string]interface{}{"fake": "yaml: line 731924: " + canary})
	if want := "the rendered text is not valid YAML"; err == nil || err.Error() != want {
		t.Fatalf("got error %v, want %q", err, want)
	}
}

func renderText(tt Tmpl, model map[string]interface{}) error {
	_, err := tt.RenderToText(model)
	return err
}

func renderMap(tt Tmpl, model map[string]interface{}) error {
	_, err := tt.RenderToMap(model)
	return err
}

func renderStringList(tt Tmpl, model map[string]interface{}) error {
	_, err := tt.RenderToStringList(model)
	return err
}

func renderBool(tt Tmpl, model map[string]interface{}) error {
	_, err := tt.RenderToBool(model, false)
	return err
}

func renderDuration(tt Tmpl, model map[string]interface{}) error {
	_, err := tt.RenderToDuration(model)
	return err
}
