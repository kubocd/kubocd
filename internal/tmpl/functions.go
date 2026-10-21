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
	"kubocd/internal/configstore"
	"kubocd/internal/misc"
	"net"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
)

// sprigFunctions lists the sprig functions available to templates. A function
// is listed only if its result depends on its arguments alone: no filesystem,
// network, environment, cluster, clock or randomness.
var sprigFunctions = []string{
	// Strings
	"indent", "lower", "nindent", "nospace", "quote", "regexReplaceAll", "replace",
	"split", "splitList", "title", "trim", "trimAll", "trimPrefix", "trimSuffix",
	"trunc", "upper",
	// Lists
	"append", "compact", "concat", "first", "has", "join", "last", "list",
	"prepend", "sortAlpha", "uniq", "until",
	// Dictionaries
	"dict", "dig", "get", "hasKey", "keys", "omit", "set",
	// Logic, defaults and types
	"coalesce", "default", "empty", "fail", "kindIs", "ternary",
	// Conversions and math
	"div", "divf", "float64", "int", "min", "mulf",
	// Encoding and URLs
	"sha256sum", "urlParse",
}

// kubocdFunctions lists the functions KuboCD defines for templates.
var kubocdFunctions = template.FuncMap{
	"fromJson":      fromJSON,
	"fromYaml":      fromYAML,
	"imageRedirect": imageRedirect,
	"required":      required,
	"toJson":        toJSON,
	"toYaml":        toYAML,
}

// Functions is a set of functions a template can call. The zero value holds
// no function.
type Functions struct {
	funcs template.FuncMap
}

var (
	releaseFunctions        = Functions{funcs: mustBuildFunctions(sprig.TxtFuncMap(), sprigFunctions, kubocdFunctions)}
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

// buildFunctions takes the functions named in names from src, and adds extra.
// The text/template builtins (and, eq, index, len, printf...) are available
// on top of the result.
func buildFunctions(src template.FuncMap, names []string, extra template.FuncMap) (template.FuncMap, error) {
	funcs := make(template.FuncMap, len(names)+len(extra))
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
	for name, fn := range extra {
		if _, ok := funcs[name]; ok {
			return nil, fmt.Errorf("template function %q is defined twice", name)
		}
		funcs[name] = fn
	}
	return funcs, nil
}

func mustBuildFunctions(src template.FuncMap, names []string, extra template.FuncMap) template.FuncMap {
	funcs, err := buildFunctions(src, names, extra)
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

// required returns val, or an error carrying msg when val is nil or an empty
// string.
func required(msg string, val interface{}) (interface{}, error) {
	if val == nil {
		return val, errors.New(msg)
	}
	if s, ok := val.(string); ok && s == "" {
		return val, errors.New(msg)
	}
	return val, nil
}

var errDNSLookupsDisabled = errors.New("resolveDNS is disabled by platform policy")

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
			return "", fmt.Errorf("resolveDNS: %s", dnsErr.Err)
		}
		return "", errors.New("resolveDNS: lookup failed")
	}
	if len(ips) == 0 {
		return "", errors.New("resolveDNS: no address")
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
// m["Error"] in the returned map.
func fromYAML(str string) map[string]interface{} {
	m := map[string]interface{}{}

	if err := yaml.Unmarshal([]byte(str), &m); err != nil {
		m["Error"] = err.Error()
	}
	return m
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

func imageRedirect(url string, scope map[string]interface{}) (map[string]interface{}, error) {
	ir := scope["ImageRedirector"]
	imageRedirector, ok := ir.(configstore.ConfigStore)
	if !ok {
		return nil, fmt.Errorf("imageRedirector is not a ConfigStore")
	}
	var imagePullSecrets []string = nil
	var imagePullPolicy = ""
	redirect, newUrl := imageRedirector.GetImageRedirect(url)
	if redirect != nil {
		imagePullSecrets = redirect.ImagePullSecrets
		imagePullPolicy = redirect.ImagePullPolicy
	}
	repo, tag, err := misc.DecodeImageUrl(newUrl)
	if err != nil {
		return nil, fmt.Errorf("imageRedirector.GetImageRedirector().DecodeImageUrl() error %w", err)
	}
	sa := strings.Split(repo, "/")
	registry := sa[0]
	baseRepository := strings.Join(sa[1:], "/")

	return map[string]interface{}{
		"url":              newUrl,
		"imagePullPolicy":  imagePullPolicy,
		"imagePullSecrets": imagePullSecrets,
		"repository":       repo,
		"tag":              tag,
		"registry":         registry,
		"baseRepository":   baseRepository,
	}, nil
}
