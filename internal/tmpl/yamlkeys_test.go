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
	"fmt"
	"kubocd/internal/misc"
	"testing"
)

var collidingDocuments = map[string]string{
	"integer and string": "1: int\n\"1\": str",
	"boolean and string": "true: bool\n\"true\": str",
	"on and string":      "on: on\n\"true\": str",
	"integer and float":  "1: int\n1.0: float",
	"nested in a list":   "outer:\n  - name: x\n    inner:\n      2: int\n      \"2\": str",
	"merged":             "base: &base {3: int}\nmerged:\n  <<: *base\n  \"3\": str",
}

func TestFromYamlFailsTheSameWayEveryTime(t *testing.T) {
	tt, err := New("", `{{ (fromYaml .src).k | toJson }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]int{}
	for range 200 {
		out, err := tt.RenderToText(map[string]interface{}{"src": "k:\n  1: int\n  \"1\": str\n"})
		outcomes[fmt.Sprintf("%q %v", out, err)]++
	}
	want := fmt.Sprintf(`"" line 1, column 5: error calling fromYaml: fromYaml: the document %v`, misc.ErrYAMLKeysCollide)
	if len(outcomes) != 1 || outcomes[want] != 200 {
		t.Fatalf("got outcomes %v, want 200 times %s", outcomes, want)
	}
}

func TestCollidingKeysAreRejected(t *testing.T) {
	for name, doc := range collidingDocuments {
		t.Run(name, func(t *testing.T) {
			tt, err := New("", "{{ .doc }}", "", ReleaseFunctions())
			if err != nil {
				t.Fatal(err)
			}
			model := map[string]interface{}{"doc": doc}
			want := fmt.Sprintf("the rendered text %v", misc.ErrYAMLKeysCollide)
			if _, err := tt.RenderToMap(model); err == nil || err.Error() != want {
				t.Errorf("RenderToMap: got error %v, want %q", err, want)
			}
			if err := tt.RenderInto(model, &map[string]interface{}{}); err == nil || err.Error() != want {
				t.Errorf("RenderInto: got error %v, want %q", err, want)
			}
			if m, err := fromYAML(doc); err == nil || err.Error() != fmt.Sprintf("fromYaml: the document %v", misc.ErrYAMLKeysCollide) {
				t.Errorf("fromYaml: got %v (%v), want an error", m, err)
			}
		})
	}
	tt, err := New("", "1: a\n2: b\n\"3\": c\nyes: d", "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	if m, err := tt.RenderToMap(map[string]interface{}{}); err != nil || len(m) != 4 {
		t.Fatalf("distinct keys: got %v (%v), want 4 entries", m, err)
	}
}

func TestCollidingKeysAreRejectedBeforeDecoding(t *testing.T) {
	docs := map[string]string{
		"infinite value":          "k:\n  1: .inf\n  \"1\": str\n",
		"NaN value":               "k:\n  1: .nan\n  \"1\": 1\n",
		"value of the wrong type": "k:\n  1: [a]\n  \"1\": str\n",
	}
	renders := map[string]struct {
		text   string
		render func(Tmpl, map[string]interface{}) (string, error)
		want   string
	}{
		"fromYaml": {`{{ (fromYaml .doc).k | toJson }}`, func(tt Tmpl, model map[string]interface{}) (string, error) {
			return tt.RenderToText(model)
		}, fmt.Sprintf("line 1, column 5: error calling fromYaml: fromYaml: the document %v", misc.ErrYAMLKeysCollide)},
		"RenderToMap": {"{{ .doc }}", func(tt Tmpl, model map[string]interface{}) (string, error) {
			m, err := tt.RenderToMap(model)
			return fmt.Sprint(m), err
		}, fmt.Sprintf("the rendered text %v", misc.ErrYAMLKeysCollide)},
		"RenderInto a typed target": {"{{ .doc }}", func(tt Tmpl, model map[string]interface{}) (string, error) {
			target := map[string]map[string]string{}
			err := tt.RenderInto(model, &target)
			return fmt.Sprint(target), err
		}, fmt.Sprintf("the rendered text %v", misc.ErrYAMLKeysCollide)},
	}
	for docName, doc := range docs {
		for renderName, r := range renders {
			t.Run(docName+", "+renderName, func(t *testing.T) {
				tt, err := New("", r.text, "", ReleaseFunctions())
				if err != nil {
					t.Fatal(err)
				}
				outcomes := map[string]int{}
				for range 200 {
					_, err := r.render(tt, map[string]interface{}{"doc": doc})
					outcomes[fmt.Sprint(err)]++
				}
				if len(outcomes) != 1 || outcomes[r.want] != 200 {
					t.Fatalf("got outcomes %v, want 200 times %q", outcomes, r.want)
				}
			})
		}
	}
}

func TestFromYamlErrorIsTheSameEveryTime(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want string
	}{
		"keys that cannot be converted": {"a:\n  ~: x\nb:\n  18446744073709551615: y\n",
			fmt.Sprintf("fromYaml: the document %v", misc.ErrYAMLKeyUnsupported)},
		"two non-finite values": {"a: .inf\nb: .nan\n", "error converting YAML to JSON: json: unsupported value: +Inf"},
		"invalid YAML":          {"a: [unclosed", "error converting YAML to JSON: yaml: line 1: did not find expected ',' or ']'"},
		"not a map": {"- a\n- b\n",
			"error unmarshaling JSON: while decoding JSON: json: cannot unmarshal array into Go value of type map[string]interface {}"},
	}
	tt, err := New("", `{{ (fromYaml .doc).Error }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			outcomes := map[string]int{}
			for range 200 {
				out, err := tt.RenderToText(map[string]interface{}{"doc": c.doc})
				outcomes[fmt.Sprintf("%s %v", out, err)]++
			}
			if want := c.want + " <nil>"; len(outcomes) != 1 || outcomes[want] != 200 {
				t.Fatalf("got outcomes %v, want 200 times %q", outcomes, want)
			}
		})
	}
}

func TestRenderedKeysThatCannotBeConvertedAreRejected(t *testing.T) {
	tt, err := New("", "{{ .doc }}", "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]int{}
	for range 200 {
		_, err := tt.RenderToMap(map[string]interface{}{"doc": "a:\n  ~: x\nb:\n  18446744073709551615: y\n"})
		outcomes[fmt.Sprint(err)]++
	}
	if want := fmt.Sprintf("the rendered text %v", misc.ErrYAMLKeyUnsupported); len(outcomes) != 1 || outcomes[want] != 200 {
		t.Fatalf("got outcomes %v, want 200 times %q", outcomes, want)
	}
}
