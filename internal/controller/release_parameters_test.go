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
package controller

import (
	"strings"
	"testing"
)

// parametersPackage takes the string parameters v and w.
const parametersPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      v:
        type: string
      w:
        type: string
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
`

// emoji is U+1F600, replacement U+FFFD, and nextLine U+0085, a line break of
// YAML that JSON lets a string hold as it is.
const (
	emoji       = string(rune(0x1F600))
	nextLine    = string(rune(0x85))
	replacement = string(rune(0xFFFD))
)

// parameterV renders the parameters given in their stored JSON form, and
// returns the parameter v.
func parameterV(t *testing.T, parameters string) (interface{}, error) {
	t.Helper()
	kcontext := map[string]interface{}{"a": "app", "b": "example.com", "apos": "it's", "nl": "line1\nline2", "lt": "<"}
	result, err := HandleParameters(newTestRelease("demo", parameters), kcontext, newTestStore(t),
		newTestPackage(t, parametersPackage))
	if err != nil {
		return nil, err
	}
	return result["v"], nil
}

func TestParametersThatRenderAsStoredRenderTheSame(t *testing.T) {
	cases := map[string]struct{ parameters, want string }{
		"action":                     {`{"v":"{{ .Context.a }}"}`, "app"},
		"backslash char constant":    {`{"v":"{{ '\\' }}{{/* ' */}}"}`, "92"},
		"newline char constant":      {`{"v":"{{ '\n' }}"}`, "10"},
		"char constant":              {`{"v":"{{ if eq (index .Context.a 0) 'a' }}yes{{ end }}"}`, "yes"},
		"raw string":                 {"{\"v\":\"{{ `a\\\\b\\\"c` }}\"}", `a\b"c`},
		"raw string holding }}":      {"{\"v\":\"{{ `}}` }}\"}", "}}"},
		"raw string compared":        {"{\"v\":\"{{ if eq `\\u003c` .Context.lt }}yes{{ end }}\"}", ""},
		"backslash outside actions":  {`{"v":"x\\y {{ .Context.a }}"}`, `x\y app`},
		"apostrophe in a value":      {`{"v":"{{ .Context.apos }}"}`, "it's"},
		"newline in a value":         {`{"v":"{{ .Context.nl }}"}`, "line1 line2"},
		"comment holding quotes":     {`{"v":"{{/* a \"comment\" */}}{{ .Context.a }}"}`, "app"},
		"trim markers and escapes":   {"{\"v\":\"{{- .Context.a -}} \\u003ctag\\u003e\"}", "app<tag>"},
		"negative number":            {`{"v":"{{-3}}"}`, "-3"},
		"adjacent actions":           {`{"v":"{{ .Context.a }}{{ .Context.b }}"}`, "appexample.com"},
		"action across two strings":  {`{"v":"{{ if true }}x","w":"{{ end }}"}`, "x"},
		"literal text with a quote":  {`{"v":"say \"{{ .Context.a }}\""}`, `say "app"`},
		"comment after trim marker":  {`{"v":"{{- /* \" */ -}}x"}`, "x"},
		"number written as a string": {`{"v":"{{ 3 }}"}`, "3"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parameterV(t, c.parameters)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got v = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAnActionOfMapWrittenParametersCanQuoteAString(t *testing.T) {
	cases := map[string]struct{ parameters, want string }{
		"printf":                      {`{"v":"{{ printf \"%s.%s\" .Context.a .Context.b }}"}`, "app.example.com"},
		"}} in a quoted string":       {`{"v":"{{ \"}}\" }}"}`, "}}"},
		"variable":                    {`{"v":"{{ $x := \"v\" }}{{ $x }}"}`, "v"},
		"newline inside an action":    {`{"v":"{{ printf \"%s\"\n.Context.a }}"}`, "app"},
		"escaped HTML characters":     {"{\"v\":\"{{ printf \\\"\\u003c%s\\u003e\\\" .Context.a }}\"}", "<app>"},
		"emoji as the API returns it": {"{\"v\":\"{{ printf \\\"%s " + emoji + "\\\" .Context.a }}\"}", "app " + emoji},
		"escaped U+FFFD":              {"{\"v\":\"{{ printf \\\"%s \\ufffd\\\" .Context.a }}\"}", "app " + replacement},
		"U+FFFD written as it is":     {"{\"v\":\"{{ printf \\\"%s " + replacement + "\\\" .Context.a }}\"}", "app " + replacement},
		"raw string compared":         {"{\"v\":\"{{ if eq `say \\\"hi\\\"` (printf \\\"say %q\\\" \\\"hi\\\") }}yes{{ end }}\"}", "yes"},
		"comment after a tab":         {`{"v":"{{-\t/* \" }} */ -}}{{ \"ok\" }}"}`, "ok"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parameterV(t, c.parameters)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got v = %q, want %q", got, c.want)
			}
		})
	}
}

// A backslash or a control character in a string of an action would be read
// differently once decoded and when rendered back through the JSON string.
func TestAnActionHoldingAnotherEscapeIsNotParsedAgain(t *testing.T) {
	cases := map[string]string{
		"backslash in both strings": "{\"v\":\"{{ eq `a\\\\b` \\\"a\\\\\\\\b\\\" }}\"}",
		"escaped quote":             `{"v":"{{ len \"a\\\"}}\" }}"}`,
		"Go escape":                 `{"v":"{{ printf \"%s\\n\" .Context.a }}"}`,
		"newline in a raw string":   "{\"v\":\"{{ printf \\\"%s\\\" `a\\nb` }}\"}",
		"tab in a quoted string":    `{"v":"{{ printf \"a\tb\" }}"}`,
		"lone surrogate":            "{\"v\":\"{{ printf \\\"%s \\ud83dx\\\" .Context.a }}\"}",
		"escaped control character": "{\"v\":\"{{ printf \\\"a\\u0001\\\" }}\"}",
		"line break written as is":  "{\"v\":\"{{ printf \\\"a" + nextLine + "b\\\" }}\"}",
	}
	for name, parameters := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parameterV(t, parameters)
			if err == nil || !strings.Contains(err.Error(), "could not create template from parameters: template: :1: ") {
				t.Fatalf("got v = %q and error %v, want the parse error of the stored text", got, err)
			}
		})
	}
}

func TestAnActionAsWrittenStillCallsOnlyTheFunctionsOfTheList(t *testing.T) {
	_, err := parameterV(t, `{"v":"{{ printf \"%s\" now }}"}`)
	if err == nil || !strings.Contains(err.Error(), `function "now" not defined`) {
		t.Fatalf("got error %v, want the function refused", err)
	}
}

func TestActionsAsWritten(t *testing.T) {
	cases := map[string]struct{ text, want string }{
		"escapes around the actions kept": {"{\"v\":\"x\\\\y \\u003c {{ \\\"a\\\" }}\"}", "{\"v\":\"x\\\\y \\u003c {{ \"a\" }}\"}"},
		"escaped surrogate pair":          {"{\"v\":\"{{ \\\"\\ud83d\\ude00\\\" }}\"}", "{\"v\":\"{{ \"" + emoji + "\" }}\"}"},
		"escaped lone surrogate":          {"{\"v\":\"{{ \\\"\\ud83dx\\\" }}\"}", "{\"v\":\"{{ \\\"\\ud83dx\\\" }}\"}"},
		"character written as it is":      {"{\"v\":\"{{ \\\"" + emoji + "\\\" }}\"}", "{\"v\":\"{{ \"" + emoji + "\" }}\"}"},
		"key and value":                   {`{"{{ \"k\" }}":"{{ \"v\" }}"}`, `{"{{ "k" }}":"{{ "v" }}"}`},
		"raw string holding }}":           {"{\"v\":\"{{ `}}\\\"` }}{{ \\\"a\\\" }}\"}", "{\"v\":\"{{ `}}\"` }}{{ \"a\" }}\"}"},
		"comment kept as stored":          {`{"v":"{{- /* \" \\ */ -}}{{ \"a\" }}"}`, `{"v":"{{- /* \" \\ */ -}}{{ "a" }}"}`},
		"comment after a newline":         {`{"v":"{{-\n/* \" }} */}}"}`, "{\"v\":\"{{-\n/* \\\" }} */}}\"}"},
		"blank in the code":               {`{"v":"{{ .a\r\n}}"}`, "{\"v\":\"{{ .a\r\n}}\"}"},
		"action left open":                {`{"v":"{{ \"a\"","w":"}}"}`, `{"v":"{{ \"a\"","w":"}}"}`},
		"not a comment after a space":     {`{"v":"{{ /* \" */ }}"}`, `{"v":"{{ /* \" */ }}"}`},
		"backslash in a string":           {`{"v":"{{ \"a\\\\b\" }}"}`, `{"v":"{{ \"a\\\\b\" }}"}`},
		"escaped control character":       {"{\"v\":\"{{ \\\"a\\u0001\\\" }}\"}", "{\"v\":\"{{ \\\"a\\u0001\\\" }}\"}"},
		"line break written as is":        {"{\"v\":\"{{ \\\"a" + nextLine + "b\\\" }}\"}", "{\"v\":\"{{ \\\"a" + nextLine + "b\\\" }}\"}"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := actionsAsWritten(c.text); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}
