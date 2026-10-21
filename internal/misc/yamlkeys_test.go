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
package misc

import (
	"errors"
	"testing"

	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

func TestCheckYAMLKeysGivesTheSameResultEveryTime(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want error
	}{
		"integer and string":               {"1: int\n\"1\": str", ErrYAMLKeysCollide},
		"boolean and string":               {"true: bool\n\"true\": str", ErrYAMLKeysCollide},
		"on and string":                    {"on: on\n\"true\": str", ErrYAMLKeysCollide},
		"integer and float":                {"1: int\n1.0: float", ErrYAMLKeysCollide},
		"two .nan":                         {".nan: a\n.nan: b", ErrYAMLKeysCollide},
		"nested in a list":                 {"outer:\n  - name: x\n    inner:\n      2: int\n      \"2\": str", ErrYAMLKeysCollide},
		"merged":                           {"base: &base {3: int}\nmerged:\n  <<: *base\n  \"3\": str", ErrYAMLKeysCollide},
		"colliding non-finite value":       {"k:\n  1: .inf\n  \"1\": str", ErrYAMLKeysCollide},
		"collision and unsupported keys":   {"a:\n  ~: x\nb:\n  1: int\n  \"1\": str", ErrYAMLKeysCollide},
		"null key":                         {"~: x", ErrYAMLKeyUnsupported},
		"null key and empty key":           {"~: x\n\"\": y", ErrYAMLKeyUnsupported},
		"null and out of range keys":       {"a:\n  ~: x\nb:\n  18446744073709551615: y", ErrYAMLKeyUnsupported},
		"distinct keys":                    {"1: a\n2: b\n\"3\": c\nyes: d\n\"\": e", nil},
		"same key twice, the last is kept": {"a: 1\na: 2", nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			outcomes := map[error]int{}
			for range 200 {
				outcomes[CheckYAMLKeys([]byte(c.doc))]++
			}
			if len(outcomes) != 1 || outcomes[c.want] != 200 {
				t.Fatalf("got outcomes %v, want 200 times %v", outcomes, c.want)
			}
		})
	}
}

func TestCheckYAMLKeysReturnsTheDecodingError(t *testing.T) {
	err := CheckYAMLKeys([]byte("a: [unclosed"))
	if err == nil || errors.Is(err, ErrYAMLKeysCollide) || errors.Is(err, ErrYAMLKeyUnsupported) {
		t.Fatalf("got %v, want the error of the YAML library", err)
	}
}

// jsonKey must give the string sigs.k8s.io/yaml makes of each kind of key.
func TestJSONKeyIsTheKeyOfSigsYAML(t *testing.T) {
	keys := []string{"name", `"quoted"`, "1", "-7", "0x1F", "0o17", "1_000", "1.5", "1.0", "1e3", "3.4028235e+38",
		"0.1", ".inf", "-.inf", ".nan", "true", "False", "on", "no", "y", "9223372036854775807"}
	for _, key := range keys {
		doc := []byte(key + ": value")
		converted := map[string]interface{}{}
		if err := yaml.Unmarshal(doc, &converted); err != nil || len(converted) != 1 {
			t.Fatalf("%s: sigs.k8s.io/yaml gives %v (%v)", key, converted, err)
		}
		typed := map[interface{}]interface{}{}
		if err := yamlv2.Unmarshal(doc, &typed); err != nil || len(typed) != 1 {
			t.Fatalf("%s: go.yaml.in/yaml/v2 gives %v (%v)", key, typed, err)
		}
		for typedKey := range typed {
			got, ok := jsonKey(typedKey)
			if _, want := converted[got]; !ok || !want {
				t.Errorf("%s: jsonKey(%#v) = %q, sigs.k8s.io/yaml gives %v", key, typedKey, got, converted)
			}
		}
	}
	for _, key := range []string{"~", "18446744073709551615"} {
		if err := yaml.Unmarshal([]byte(key+": value"), &map[string]interface{}{}); err == nil {
			t.Errorf("%s: sigs.k8s.io/yaml converts it, jsonKey does not", key)
		}
		typed := map[interface{}]interface{}{}
		if err := yamlv2.Unmarshal([]byte(key+": value"), &typed); err != nil {
			t.Fatal(err)
		}
		for typedKey := range typed {
			if _, ok := jsonKey(typedKey); ok {
				t.Errorf("%s: jsonKey converts %#v", key, typedKey)
			}
		}
	}
}
