/*
Copyright 2025 Kubotal

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
	"encoding/json"
	"errors"
	"fmt"
	"kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"net"
	"net/url"
	"slices"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
)

// sprigFunctions lists the sprig functions available to templates. A function
// is listed only if its result depends on its arguments alone: no filesystem,
// network, environment, cluster, clock or randomness.
var sprigFunctions = []string{
	// Strings
	"lower", "nospace", "quote", "regexReplaceAll",
	"split", "splitList", "title", "trim", "trimAll", "trimPrefix", "trimSuffix",
	"trunc", "upper",
	// Lists
	"append", "compact", "concat", "first", "has", "join", "last", "list",
	"prepend", "sortAlpha", "uniq",
	// Dictionaries
	"dict", "dig", "get", "hasKey", "omit", "set",
	// Logic, defaults and types
	"coalesce", "default", "empty", "kindIs", "ternary",
	// Conversions and math
	"div", "divf", "float64", "int", "min", "mulf",
	// Encoding
	"sha256sum",
}

// kubocdFunctions lists the functions KuboCD defines for templates.
var kubocdFunctions = template.FuncMap{
	"fail":          fail,
	"fromJson":      fromJSON,
	"fromYaml":      fromYAML,
	"imageRedirect": imageRedirect,
	"indent":        indent,
	"keys":          keys,
	"nindent":       nindent,
	"replace":       replace,
	"required":      required,
	"toJson":        toJSON,
	"toYaml":        toYAML,
	"until":         until,
	"urlParse":      urlParse,
}

// builtinOverrides replace the text/template builtins of the same name.
var builtinOverrides = template.FuncMap{
	"printf": printf,
}

// Functions is a set of functions a template can call. New rejects the zero
// value, which would leave the text/template builtins, printf among them,
// without the KuboCD versions.
type Functions struct {
	funcs template.FuncMap
}

var errNoFunctions = errors.New("a template needs a function set (tmpl.ReleaseFunctions or tmpl.PackageFunctions)")

var (
	releaseFunctions        = Functions{funcs: mustBuildFunctions(sprig.TxtFuncMap(), sprigFunctions, kubocdFunctions, builtinOverrides)}
	packageFunctions        = Functions{funcs: withFunction(releaseFunctions.funcs, "resolveDNS", resolveDNSDisabled)}
	packageFunctionsWithDNS = Functions{funcs: withFunction(releaseFunctions.funcs, "resolveDNS", resolveDNS)}
)

// ReleaseFunctions returns the functions available to the parameters of a
// Release.
func ReleaseFunctions() Functions {
	return releaseFunctions
}

// PackageFunctions returns the functions available to the templates of a
// package: the Release functions plus resolveDNS. resolveDNS is always
// defined, so that a package parses the same way everywhere, but it only
// resolves a name when allowDNSLookups is set.
func PackageFunctions(allowDNSLookups bool) Functions {
	if allowDNSLookups {
		return packageFunctionsWithDNS
	}
	return packageFunctions
}

// buildFunctions takes the functions named in names from src, and adds the
// extras. The text/template builtins (and, eq, index, len...) remain available
// unless an extra replaces them.
func buildFunctions(src template.FuncMap, names []string, extras ...template.FuncMap) (template.FuncMap, error) {
	funcs := make(template.FuncMap, len(names))
	for _, name := range names {
		fn, ok := src[name]
		if !ok {
			return nil, fmt.Errorf("template function %q is not provided by sprig", name)
		}
		if _, ok := funcs[name]; ok {
			return nil, fmt.Errorf("template function %q is listed twice", name)
		}
		funcs[name] = fn
	}
	for _, extra := range extras {
		for name, fn := range extra {
			if _, ok := funcs[name]; ok {
				return nil, fmt.Errorf("template function %q is defined twice", name)
			}
			funcs[name] = fn
		}
	}
	return funcs, nil
}

func mustBuildFunctions(src template.FuncMap, names []string, extras ...template.FuncMap) template.FuncMap {
	funcs, err := buildFunctions(src, names, extras...)
	if err != nil {
		panic(err)
	}
	return funcs
}

func withFunction(funcs template.FuncMap, name string, fn interface{}) template.FuncMap {
	out := make(template.FuncMap, len(funcs)+1)
	for k, v := range funcs {
		out[k] = v
	}
	out[name] = fn
	return out
}

// shownError is an error message that render errors keep as written: a
// message of a KuboCD function, which holds no template data, or a message
// the template author writes with fail or required.
type shownError struct {
	msg string
}

func (e *shownError) Error() string {
	return e.msg
}

func shownf(format string, args ...interface{}) error {
	return &shownError{msg: fmt.Sprintf(format, args...)}
}

// fail is the sprig function: it stops the render with msg.
func fail(msg string) (string, error) {
	return "", &shownError{msg: msg}
}

// required returns val, or an error carrying msg when val is nil or an empty
// string.
func required(msg string, val interface{}) (interface{}, error) {
	if val == nil {
		return val, &shownError{msg: msg}
	}
	if s, ok := val.(string); ok && s == "" {
		return val, &shownError{msg: msg}
	}
	return val, nil
}

// printf is fmt.Sprintf, like the text/template builtin, except that it
// rejects the %p verb: the memory address it prints changes from one render
// to the next.
func printf(format string, args ...interface{}) (string, error) {
	if hasPointerVerb(format) {
		return "", shownf("printf: the %%p verb is not allowed")
	}
	return fmt.Sprintf(format, args...), nil
}

var (
	sprigIndent  = sprig.TxtFuncMap()["indent"].(func(int, string) string)
	sprigNindent = sprig.TxtFuncMap()["nindent"].(func(int, string) string)
	sprigReplace = sprig.TxtFuncMap()["replace"].(func(string, string, string) string)
)

// textResultLimit reports an oversized function result without template data.
func textResultLimit(name string) error {
	return &limitError{msg: fmt.Sprintf("%s: the result is larger than %d bytes", name, maxRenderedText)}
}

// checkIndentation checks whether indentation is valid within maxRenderedText.
func checkIndentation(name string, width int, value string, leadingNewline int) error {
	if width < 0 {
		return &limitError{msg: name + ": the indentation must not be negative"}
	}
	remaining := maxRenderedText - leadingNewline
	if len(value) > remaining || width > (remaining-len(value))/(strings.Count(value, "\n")+1) {
		return textResultLimit(name)
	}
	return nil
}

// indent is the sprig function, except that it refuses negative widths and results beyond maxRenderedText.
func indent(width int, value string) (string, error) {
	if err := checkIndentation("indent", width, value, 0); err != nil {
		return "", err
	}
	return sprigIndent(width, value), nil
}

// nindent is the sprig function, except that it refuses negative widths and results beyond maxRenderedText.
func nindent(width int, value string) (string, error) {
	if err := checkIndentation("nindent", width, value, 1); err != nil {
		return "", err
	}
	return sprigNindent(width, value), nil
}

// replace is the sprig function, except that it refuses results beyond maxRenderedText.
func replace(old, replacement, source string) (string, error) {
	remainingSource := len(source)
	var count int
	if old == "" {
		if remainingSource > maxRenderedText {
			return "", textResultLimit("replace")
		}
		// An empty search inserts at UTF-8 boundaries, including both ends.
		count = utf8.RuneCountInString(source) + 1
	} else {
		count = strings.Count(source, old)
		// Nonoverlapping matches occupy at most len(source) bytes.
		remainingSource -= count * len(old)
	}
	if remainingSource > maxRenderedText || count > 0 && len(replacement) > (maxRenderedText-remainingSource)/count {
		return "", textResultLimit("replace")
	}
	return sprigReplace(old, replacement, source), nil
}

// maxUntil is the most items that until builds, either way.
const maxUntil = 10000

var sprigUntil = sprig.TxtFuncMap()["until"].(func(int) []int)

// until is the sprig function, except that it refuses a count beyond
// maxUntil either way, as it allocates the whole list.
func until(count int) ([]int, error) {
	if count > maxUntil || count < -maxUntil {
		return nil, &limitError{msg: fmt.Sprintf("until: the count is beyond the limit of %d", maxUntil)}
	}
	return sprigUntil(count), nil
}

var sprigURLParse = sprig.TxtFuncMap()["urlParse"].(func(string) map[string]interface{})

// urlParse is the sprig function, except that an invalid URL is an error
// without the URL, which can hold credentials.
func urlParse(v string) (map[string]interface{}, error) {
	if _, err := url.Parse(v); err != nil {
		return nil, shownf("urlParse: invalid URL")
	}
	return sprigURLParse(v), nil
}

// keys returns the keys of the given dicts, sorted. A key present in several
// dicts appears once per dict.
func keys(dicts ...map[string]interface{}) []string {
	k := []string{}
	for _, dict := range dicts {
		for key := range dict {
			k = append(k, key)
		}
	}
	slices.Sort(k)
	return k
}

var errDNSLookupsDisabled = shownf("resolveDNS is disabled by platform policy")

func resolveDNSDisabled(string) (string, error) {
	return "", errDNSLookupsDisabled
}

// resolveDNS returns the first address of a host name.
func resolveDNS(host string) (string, error) {
	ips, err := net.LookupIP(host)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			// The message of a DNSError quotes the host name, which can be data
			return "", shownf("resolveDNS: %s", dnsErr.Err)
		}
		return "", shownf("resolveDNS: lookup failed")
	}
	if len(ips) == 0 {
		return "", shownf("resolveDNS: no address")
	}
	return ips[0].String(), nil
}

// toYAML, fromYAML, toJSON and fromJSON come from Helm (pkg/engine/funcs.go),
// Copyright The Helm Authors, Apache License 2.0.

// toYAML takes an interface, marshals it to yaml, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toYAML(v interface{}) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside a template.
		return fmt.Sprintf("!!!!!! ERROR: %v on %#v", err, v)
	}
	return strings.TrimSuffix(string(data), "\n")
}

// fromYAML converts a YAML document into a map[string]interface{}.
//
// This is not a general-purpose YAML parser, and will not parse all valid
// YAML documents. Additionally, because its intended use is within templates
// it tolerates errors. It will insert the returned error message string into
// m["Error"] in the returned map. A document with keys that are the same once
// converted to strings is an error, as the map would keep one of their values
// at random. The keys are checked before they are converted, so that the
// result never depends on their order.
func fromYAML(str string) (map[string]interface{}, error) {
	m := map[string]interface{}{}

	switch err := misc.CheckYAMLKeys([]byte(str)); {
	case errors.Is(err, misc.ErrYAMLKeysCollide):
		return nil, shownf("fromYaml: the document %v", err)
	case errors.Is(err, misc.ErrYAMLKeyUnsupported):
		m["Error"] = fmt.Sprintf("fromYaml: the document %v", err)
		return m, nil
	}
	if err := yaml.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m, nil
}

// toJSON takes an interface, marshals it to json, and returns a string. It will
// always return a string, even on marshal error (empty string).
//
// This is designed to be called from a template.
func toJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Swallow errors inside a template.
		return fmt.Sprintf("!!!!!! ERROR: %v", err)
	}
	return string(data)
}

// fromJSON converts a JSON document into a map[string]interface{}.
//
// This is not a general-purpose JSON parser, and will not parse all valid
// JSON documents. Additionally, because its intended use is within templates
// it tolerates errors. It will insert the returned error message string into
// m["Error"] in the returned map.
func fromJSON(str string) map[string]interface{} {
	m := make(map[string]interface{})

	if err := json.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
}

// ImageRedirectsKey is the entry of the template model holding the image
// redirect table.
const ImageRedirectsKey = "ImageRedirects"

// ImageRedirectTable turns image redirects into the plain data imageRedirect
// reads from the template model.
func ImageRedirectTable(redirects []*v1alpha1.ImageRedirectSpec) []interface{} {
	table := make([]interface{}, 0, len(redirects))
	for _, r := range redirects {
		table = append(table, map[string]interface{}{
			"oldPrefix":        r.OldPrefix,
			"newPrefix":        r.NewPrefix,
			"imagePullPolicy":  r.ImagePullPolicy,
			"imagePullSecrets": slices.Clone(r.ImagePullSecrets),
		})
	}
	return table
}

// imageRedirect applies to url the first entry of the image redirect table of
// scope whose oldPrefix matches, and splits the resulting image reference. An
// empty newPrefix keeps the URL.
func imageRedirect(url string, scope map[string]interface{}) (map[string]interface{}, error) {
	table, ok := scope[ImageRedirectsKey].([]interface{})
	if !ok {
		return nil, shownf("imageRedirect: the scope has no image redirect table, pass the root context ('.' or '$')")
	}
	newURL := url
	var imagePullSecrets []string
	imagePullPolicy := ""
	for idx, entry := range table {
		redirect, ok := entry.(map[string]interface{})
		if !ok {
			return nil, shownf("imageRedirect: entry %d of the image redirect table is not a map", idx)
		}
		oldPrefix, ok := redirect["oldPrefix"].(string)
		if !ok {
			return nil, shownf("imageRedirect: entry %d of the image redirect table has no oldPrefix", idx)
		}
		if !strings.HasPrefix(url, oldPrefix) {
			continue
		}
		newPrefix, err := optionalString(redirect, "newPrefix")
		if err != nil {
			return nil, shownf("imageRedirect: entry %d of the image redirect table: %v", idx, err)
		}
		if newPrefix != "" {
			newURL = newPrefix + url[len(oldPrefix):]
		}
		if imagePullPolicy, err = optionalString(redirect, "imagePullPolicy"); err != nil {
			return nil, shownf("imageRedirect: entry %d of the image redirect table: %v", idx, err)
		}
		if imagePullSecrets, err = optionalStringList(redirect, "imagePullSecrets"); err != nil {
			return nil, shownf("imageRedirect: entry %d of the image redirect table: %v", idx, err)
		}
		break
	}
	repo, tag, err := misc.DecodeImageUrl(newURL)
	if err != nil {
		if newURL != url {
			return nil, shownf("imageRedirect: the redirected image reference is invalid, check the newPrefix of the matching redirect")
		}
		return nil, shownf("imageRedirect: the image reference is invalid")
	}
	sa := strings.Split(repo, "/")
	registry := sa[0]
	baseRepository := strings.Join(sa[1:], "/")

	return map[string]interface{}{
		"url":              newURL,
		"imagePullPolicy":  imagePullPolicy,
		"imagePullSecrets": imagePullSecrets,
		"repository":       repo,
		"tag":              tag,
		"registry":         registry,
		"baseRepository":   baseRepository,
	}, nil
}

func optionalString(m map[string]interface{}, key string) (string, error) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", key)
	}
	return s, nil
}

func optionalStringList(m map[string]interface{}, key string) ([]string, error) {
	switch v := m[key].(type) {
	case nil:
		return nil, nil
	case []string:
		return slices.Clone(v), nil
	case []interface{}:
		list := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s is not a list of strings", key)
			}
			list = append(list, s)
		}
		return list, nil
	default:
		return nil, fmt.Errorf("%s is not a list of strings", key)
	}
}
