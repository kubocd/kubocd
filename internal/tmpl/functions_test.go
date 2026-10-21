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
	"bytes"
	"fmt"
	"kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"maps"
	"net"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Changing this list changes what every package and Release template can do.
var expectedFunctions = []string{
	"append", "coalesce", "compact", "concat", "default", "dict", "dig", "div",
	"divf", "empty", "fail", "first", "float64", "fromJson", "fromYaml", "get",
	"has", "hasKey", "imageRedirect", "indent", "int", "join", "keys", "kindIs",
	"last", "list", "lower", "min", "mulf", "nindent", "nospace", "omit",
	"prepend", "quote", "regexReplaceAll", "replace", "required", "set",
	"sha256sum", "sortAlpha", "split", "splitList", "ternary", "title",
	"toJson", "toYaml", "trim", "trimAll", "trimPrefix", "trimSuffix", "trunc",
	"uniq", "until", "upper", "urlParse",
}

// packageOnlyFunctions are available to package templates, never to Release
// parameters.
var packageOnlyFunctions = []string{"resolveDNS"}

func functionNames(f Functions) []string {
	return slices.Sorted(maps.Keys(f.funcs))
}

func sortedCopy(lists ...[]string) []string {
	return slices.Sorted(slices.Values(slices.Concat(lists...)))
}

func TestFunctionListIsFrozen(t *testing.T) {
	sets := map[string]struct {
		functions Functions
		want      []string
	}{
		"release":              {ReleaseFunctions(), sortedCopy(expectedFunctions)},
		"package":              {PackageFunctions(false), sortedCopy(expectedFunctions, packageOnlyFunctions)},
		"package with DNS set": {PackageFunctions(true), sortedCopy(expectedFunctions, packageOnlyFunctions)},
	}
	for name, set := range sets {
		if got := functionNames(set.functions); !slices.Equal(got, set.want) {
			t.Errorf("%s functions differ from the frozen list\ngot:  %v\nwant: %v", name, got, set.want)
		}
	}
}

func TestExcludedFunctionsAreRejected(t *testing.T) {
	excluded := []string{
		"env", "expandenv", "getHostByName", "now", "date", "dateInZone", "randAlphaNum",
		"randBytes", "uuidv4", "genPrivateKey", "genCA", "htpasswd", "derivePassword",
		"readFile", "lookup", "include", "tpl", "toToml", "fromYamlArray", "fromJsonArray",
	}
	for name := range sprig.TxtFuncMap() {
		if slices.Contains(expectedFunctions, name) {
			continue
		}
		// Skip the sprig names that are also text/template builtins
		if _, err := template.New("").Parse(fmt.Sprintf("{{ %s }}", name)); err == nil {
			continue
		}
		excluded = append(excluded, name)
	}
	check := func(set string, functions Functions, name string) {
		t.Helper()
		_, err := New("", fmt.Sprintf("value: {{ %s }}", name), "", functions)
		if err == nil {
			t.Errorf("%s: %s: parsing should fail", set, name)
			return
		}
		if want := fmt.Sprintf("function %q not defined", name); !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %s: error %q does not contain %q", set, name, err.Error(), want)
		}
	}
	for _, name := range excluded {
		check("release", ReleaseFunctions(), name)
		check("package", PackageFunctions(true), name)
	}
	for _, name := range packageOnlyFunctions {
		check("release", ReleaseFunctions(), name)
	}
}

func TestAllowedFunctionsWork(t *testing.T) {
	cases := map[string]struct {
		text string
		want string
	}{
		"append":          {`{{ append (list 1) 2 | toJson }}`, "[1,2]"},
		"coalesce":        {`{{ coalesce "" .missing "x" }}`, "x"},
		"compact":         {`{{ compact (list "a" "" "b") | toJson }}`, `["a","b"]`},
		"concat":          {`{{ concat (list 1) (list 2 3) | toJson }}`, "[1,2,3]"},
		"default":         {`{{ .missing | default "d" }}`, "d"},
		"dict":            {`{{ dict "a" 1 | toJson }}`, `{"a":1}`},
		"dig":             {`{{ dig "a" "b" "none" (dict "a" (dict "b" "v")) }}`, "v"},
		"div":             {`{{ div 7 2 }}`, "3"},
		"divf":            {`{{ divf 7 2 }}`, "3.5"},
		"empty":           {`{{ empty "" }}`, "true"},
		"fail":            {`{{ if false }}{{ fail "never" }}{{ end }}ok`, "ok"},
		"first":           {`{{ first (list "a" "b") }}`, "a"},
		"float64":         {`{{ float64 "1.5" }}`, "1.5"},
		"fromJson":        {`{{ (fromJson "{\"a\":\"b\"}").a }}`, "b"},
		"fromYaml":        {`{{ (fromYaml "a: b").a }}`, "b"},
		"get":             {`{{ get (dict "a" "v") "a" }}`, "v"},
		"has":             {`{{ has 2 (list 1 2) }}`, "true"},
		"hasKey":          {`{{ hasKey (dict "a" 1) "a" }}`, "true"},
		"imageRedirect":   {`{{ (imageRedirect "quay.io/org/img:1.0" .).registry }}`, "quay.io"},
		"indent":          {`{{ indent 2 "a" }}`, "  a"},
		"int":             {`{{ int "42" }}`, "42"},
		"join":            {`{{ join "," (list "a" "b") }}`, "a,b"},
		"keys":            {`{{ keys (dict "a" 1) | toJson }}`, `["a"]`},
		"kindIs":          {`{{ kindIs "map" (dict) }}`, "true"},
		"last":            {`{{ last (list "a" "b") }}`, "b"},
		"list":            {`{{ list 1 2 | toJson }}`, "[1,2]"},
		"lower":           {`{{ lower "ABC" }}`, "abc"},
		"min":             {`{{ min 4 2 3 }}`, "2"},
		"mulf":            {`{{ mulf 1.5 2 }}`, "3"},
		"nindent":         {`{{ nindent 2 "a" }}`, "\n  a"},
		"nospace":         {`{{ nospace "a b c" }}`, "abc"},
		"omit":            {`{{ omit (dict "a" 1 "b" 2) "a" | toJson }}`, `{"b":2}`},
		"prepend":         {`{{ prepend (list 2) 1 | toJson }}`, "[1,2]"},
		"quote":           {`{{ quote "a" }}`, `"a"`},
		"regexReplaceAll": {`{{ regexReplaceAll "[0-9]" "a1b2" "-" }}`, "a-b-"},
		"replace":         {`{{ replace "." "-" "a.b" }}`, "a-b"},
		"required":        {`{{ required "needed" "v" }}`, "v"},
		"set":             {`{{ $d := dict }}{{ $_ := set $d "a" 1 }}{{ toJson $d }}`, `{"a":1}`},
		"sha256sum":       {`{{ sha256sum "a" }}`, "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"},
		"sortAlpha":       {`{{ sortAlpha (list "b" "a") | toJson }}`, `["a","b"]`},
		"split":           {`{{ (split "." "a.b")._1 }}`, "b"},
		"splitList":       {`{{ splitList "." "a.b" | toJson }}`, `["a","b"]`},
		"ternary":         {`{{ ternary "y" "n" true }}`, "y"},
		"title":           {`{{ title "hello world" }}`, "Hello World"},
		"toJson":          {`{{ toJson (list "a") }}`, `["a"]`},
		"toYaml":          {`{{ toYaml (dict "a" 1) }}`, "a: 1"},
		"trim":            {`{{ trim "  a  " }}`, "a"},
		"trimAll":         {`{{ trimAll "-" "--a--" }}`, "a"},
		"trimPrefix":      {`{{ trimPrefix "v" "v1.0" }}`, "1.0"},
		"trimSuffix":      {`{{ trimSuffix ".csv" "data.csv" }}`, "data"},
		"trunc":           {`{{ trunc 3 "abcdef" }}`, "abc"},
		"uniq":            {`{{ uniq (list 1 1 2) | toJson }}`, "[1,2]"},
		"until":           {`{{ until 3 | toJson }}`, "[0,1,2]"},
		"upper":           {`{{ upper "abc" }}`, "ABC"},
		"urlParse":        {`{{ (urlParse "https://h:8443/p").host }}`, "h:8443"},
	}
	if got, want := slices.Sorted(maps.Keys(cases)), sortedCopy(expectedFunctions); !slices.Equal(got, want) {
		t.Fatalf("test cases differ from the frozen list\ngot:  %v\nwant: %v", got, want)
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, "", ReleaseFunctions())
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			got, err := tt.RenderToText(map[string]interface{}{ImageRedirectsKey: ImageRedirectTable(nil)})
			if err != nil {
				t.Fatalf("render error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFailAndRequiredReportTheAuthorMessage(t *testing.T) {
	cases := map[string]struct {
		text string
		want string
	}{
		"fail":                   {`{{ fail "the bucket is mandatory" }}`, "error calling fail: the bucket is mandatory"},
		"required, missing":      {`{{ required "a host is required" .missing }}`, "error calling required: a host is required"},
		"required, empty string": {`{{ required "a host is required" .empty }}`, "error calling required: a host is required"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", c.text, "", ReleaseFunctions())
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			_, err = tt.RenderToText(map[string]interface{}{"empty": ""})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestResolveDNSIsDisabledByDefault(t *testing.T) {
	tt, err := New("", `ip: {{ resolveDNS "localhost" }}`, "", PackageFunctions(false))
	if err != nil {
		t.Fatalf("a package calling resolveDNS must parse: %v", err)
	}
	_, err = tt.RenderToText(map[string]interface{}{})
	if want := "resolveDNS is disabled by platform policy"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

func TestResolveDNSResolvesWhenAllowed(t *testing.T) {
	if _, err := net.LookupIP("localhost"); err != nil {
		t.Skipf("localhost does not resolve on this machine: %v", err)
	}
	tt, err := New("", `{{ resolveDNS "localhost" }}`, "", PackageFunctions(true))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	got, err := tt.RenderToText(map[string]interface{}{})
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if ip := net.ParseIP(got); ip == nil || !ip.IsLoopback() {
		t.Fatalf("got %q, want a loopback address", got)
	}
}

func TestBuildFunctionsRejectsAnInconsistentList(t *testing.T) {
	src := template.FuncMap{"a": strings.ToUpper, "b": strings.ToLower}
	cases := map[string]struct {
		names []string
		extra template.FuncMap
		want  string
	}{
		"unknown name":     {[]string{"a", "c"}, nil, `"c" is not provided`},
		"name twice":       {[]string{"a", "a"}, nil, `"a" is listed twice`},
		"extra overriding": {[]string{"a"}, template.FuncMap{"a": strings.TrimSpace}, `"a" is defined twice`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildFunctions(src, c.names, c.extra)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestTextTemplateBuiltinsAreAvailable(t *testing.T) {
	text := `{{ printf "%s-%d" "a" 1 }} {{ len .l }} {{ index .l 0 }} {{ slice "abc" 1 }} ` +
		`{{ eq 1 1 }} {{ ne 1 2 }} {{ lt 1 2 }} {{ le 1 1 }} {{ gt 2 1 }} {{ ge 1 1 }} ` +
		`{{ and true false }} {{ or false true }} {{ not false }} {{ print "p" }} {{ println "q" }}` +
		`{{ html "<" }} {{ js "'" }} {{ urlquery "a b" }}`
	model := map[string]interface{}{"l": []string{"x"}}
	want := `a-1 1 x bc true true true true true true false true true p q
&lt; \' a+b`

	bare, err := template.New("").Parse(text)
	if err != nil {
		t.Fatalf("builtins need no FuncMap: %v", err)
	}
	buf := &bytes.Buffer{}
	if err := bare.Execute(buf, model); err != nil {
		t.Fatalf("render error: %v", err)
	}
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}

	tt, err := New("", text, "", ReleaseFunctions())
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	got, err := tt.RenderToText(model)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestKubocdToJsonOverridesSprig(t *testing.T) {
	toJson, ok := ReleaseFunctions().funcs["toJson"].(func(interface{}) string)
	if !ok {
		t.Fatalf("toJson is not the KuboCD function: %T", ReleaseFunctions().funcs["toJson"])
	}
	if got := toJson(make(chan int)); !strings.HasPrefix(got, "!!!!!! ERROR:") {
		t.Fatalf("toJson should report marshal errors, got %q", got)
	}
}

func TestImageRedirect(t *testing.T) {
	store := configstore.New()
	err := store.AddConfigs(&v1alpha1.ConfigList{Items: []v1alpha1.Config{
		{ObjectMeta: metav1.ObjectMeta{Name: "b-config"}, Spec: v1alpha1.ConfigSpec{ImageRedirects: []*v1alpha1.ImageRedirectSpec{
			{OldPrefix: "docker.io/", NewPrefix: "other.local/", ImagePullPolicy: "IfNotPresent"},
			{OldPrefix: "ghcr.io/", ImagePullPolicy: "Always", ImagePullSecrets: []string{"ghcr-pull"}},
		}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "a-config"}, Spec: v1alpha1.ConfigSpec{ImageRedirects: []*v1alpha1.ImageRedirectSpec{
			{OldPrefix: "docker.io/bitnami/", NewPrefix: "mirror.local:5000/bitnami/", ImagePullPolicy: "Always",
				ImagePullSecrets: []string{"mirror-pull", "extra"}},
		}}},
	}}, "kubocd")
	if err != nil {
		t.Fatal(err)
	}
	// Configs apply in name order: a-config wins over b-config for docker.io/bitnami/
	cases := []struct {
		url  string
		want string
	}{
		{"docker.io/bitnami/redis:7.2.4", `{"baseRepository":"bitnami/redis","imagePullPolicy":"Always","imagePullSecrets":["mirror-pull","extra"],"registry":"mirror.local:5000","repository":"mirror.local:5000/bitnami/redis","tag":"7.2.4","url":"mirror.local:5000/bitnami/redis:7.2.4"}`},
		{"docker.io/bitnami/redis", `{"baseRepository":"bitnami/redis","imagePullPolicy":"Always","imagePullSecrets":["mirror-pull","extra"],"registry":"mirror.local:5000","repository":"mirror.local:5000/bitnami/redis","tag":"latest","url":"mirror.local:5000/bitnami/redis"}`},
		{"docker.io/library/nginx:1.27", `{"baseRepository":"library/nginx","imagePullPolicy":"IfNotPresent","imagePullSecrets":null,"registry":"other.local","repository":"other.local/library/nginx","tag":"1.27","url":"other.local/library/nginx:1.27"}`},
		{"ghcr.io/org/tool:v1", `{"baseRepository":"org/tool","imagePullPolicy":"Always","imagePullSecrets":["ghcr-pull"],"registry":"ghcr.io","repository":"ghcr.io/org/tool","tag":"v1","url":"ghcr.io/org/tool:v1"}`},
		{"quay.io/jetstack/cert-manager-controller:v1.17.0", `{"baseRepository":"jetstack/cert-manager-controller","imagePullPolicy":"","imagePullSecrets":null,"registry":"quay.io","repository":"quay.io/jetstack/cert-manager-controller","tag":"v1.17.0","url":"quay.io/jetstack/cert-manager-controller:v1.17.0"}`},
		{"registry.local:5000/team/app:2.0", `{"baseRepository":"team/app","imagePullPolicy":"","imagePullSecrets":null,"registry":"registry.local:5000","repository":"registry.local:5000/team/app","tag":"2.0","url":"registry.local:5000/team/app:2.0"}`},
	}
	render := func(url string, model map[string]interface{}) (string, error) {
		tt, err := New("", fmt.Sprintf("{{ toJson (imageRedirect %q .) }}", url), "", PackageFunctions(false))
		if err != nil {
			t.Fatal(err)
		}
		return tt.RenderToText(model)
	}
	model := map[string]interface{}{ImageRedirectsKey: ImageRedirectTable(store.GetImageRedirects())}
	for _, c := range cases {
		got, err := render(c.url, model)
		if err != nil {
			t.Errorf("%s: %v", c.url, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\ngot:  %s\nwant: %s", c.url, got, c.want)
		}
	}

	got, err := render("quay.io/org/img:1.0", map[string]interface{}{ImageRedirectsKey: ImageRedirectTable(nil)})
	if want := `{"baseRepository":"org/img","imagePullPolicy":"","imagePullSecrets":null,"registry":"quay.io","repository":"quay.io/org/img","tag":"1.0","url":"quay.io/org/img:1.0"}`; err != nil || got != want {
		t.Errorf("without redirects: got %q (%v), want %q", got, err, want)
	}
	generic := []interface{}{map[string]interface{}{"oldPrefix": "quay.io/", "imagePullSecrets": []interface{}{"a", "b"}}}
	got, err = render("quay.io/org/img:1.0", map[string]interface{}{ImageRedirectsKey: generic})
	if want := `["a","b"]`; err != nil || !strings.Contains(got, want) {
		t.Errorf("generic list of pull secrets: got %q (%v), want it to contain %q", got, err, want)
	}

	failures := map[string]struct {
		url   string
		model map[string]interface{}
		want  string
	}{
		"no table":      {"quay.io/org/img:1.0", map[string]interface{}{}, "pass the root context"},
		"invalid image": {"a:b", model, "imageRedirect: the image reference is invalid"},
		"invalid image after redirection": {"docker.io/org/img:1.0",
			map[string]interface{}{ImageRedirectsKey: []interface{}{map[string]interface{}{"oldPrefix": "docker.io/", "newPrefix": "a:b/c:d:"}}},
			"imageRedirect: the redirected image reference is invalid"},
		"entry not a map": {"quay.io/org/img:1.0",
			map[string]interface{}{ImageRedirectsKey: []interface{}{"quay.io/", map[string]interface{}{"oldPrefix": "quay.io/"}}},
			"entry 0 of the image redirect table is not a map"},
		"entry without oldPrefix": {"quay.io/org/img:1.0",
			map[string]interface{}{ImageRedirectsKey: []interface{}{map[string]interface{}{"newPrefix": "mirror.local/"}}},
			"entry 0 of the image redirect table has no oldPrefix"},
		"pull secrets not strings": {"quay.io/org/img:1.0",
			map[string]interface{}{ImageRedirectsKey: []interface{}{map[string]interface{}{"oldPrefix": "quay.io/", "imagePullSecrets": []interface{}{1}}}},
			"imagePullSecrets is not a list of strings"},
	}
	for name, c := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := render(c.url, c.model)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
		})
	}
}

const realisticValues = `
{{- $full := printf "%s-main" .Release.metadata.name }}
image:
  {{- $redirection := imageRedirect "quay.io/jetstack/cert-manager-controller:v1.17.0" . }}
  repository: {{ $redirection.repository }}
  tag: {{ $redirection.tag }}
fullnameOverride: {{ $full }}
replicas: {{ .Parameters.replicas | default 1 }}
checksum: {{ toJson (.Parameters.grants | default list) | sha256sum | quote }}
{{- if gt (int .Parameters.replicas) 1 }}
retryJoin:
{{- range $i := until (int .Parameters.replicas) }}
  - "http://{{ $full }}-{{ $i }}.{{ $full }}-internal:8200"
{{- end }}
{{- end }}
host: {{ printf "%s.%s" .Release.metadata.name .Context.ingress.domain | quote }}
{{- with .Parameters.extraLabels }}
labels:
  {{- toYaml . | nindent 2 }}
{{- end }}
groups: {{ .Parameters.groups | default (list) | toJson }}
adminPassword: {{ required "adminPassword is required" .Parameters.adminPassword | quote }}
roles: {{ splitList "," "a, b" | join "," | replace " " "" | upper | quote }}
`

func TestRealisticPackageTemplate(t *testing.T) {
	tt, err := New("", realisticValues, "", PackageFunctions(false))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	model := map[string]interface{}{
		"Release": map[string]interface{}{"metadata": map[string]interface{}{"name": "vault"}},
		"Context": map[string]interface{}{"ingress": map[string]interface{}{"domain": "example.com"}},
		"Parameters": map[string]interface{}{
			"replicas":      3,
			"grants":        []interface{}{"read"},
			"extraLabels":   map[string]interface{}{"team": "data"},
			"adminPassword": "secret",
		},
		ImageRedirectsKey: ImageRedirectTable(nil),
	}
	got, err := tt.RenderToText(model)
	if err != nil {
		t.Fatalf("render error: %v", err)
	}
	want := `
image:
  repository: quay.io/jetstack/cert-manager-controller
  tag: v1.17.0
fullnameOverride: vault-main
replicas: 3
checksum: "8a2d0b87e8c712cc8e06c5c3f8de5e7f9dc8edcca03079a7aec0cb6436294927"
retryJoin:
  - "http://vault-main-0.vault-main-internal:8200"
  - "http://vault-main-1.vault-main-internal:8200"
  - "http://vault-main-2.vault-main-internal:8200"
host: "vault.example.com"
labels:
  team: data
groups: []
adminPassword: "secret"
roles: "A,B"
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
