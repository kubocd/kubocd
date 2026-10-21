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
	"strings"
	"testing"
	"time"
)

type namedString string

func TestTemplatesReceivePlainDataOnly(t *testing.T) {
	tt, err := New("", `{{ .Context }}`, "", ReleaseFunctions())
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]interface{}{
		"Context": map[string]interface{}{
			"s": "a", "b": true, "i": 1, "i64": int64(2), "u": uint8(3), "f": 1.5, "nil": nil,
			"list":    []interface{}{"a", 1, map[string]interface{}{"k": "v"}},
			"strings": []string{"a"},
			"maps":    []map[string]interface{}{{"k": []interface{}{}}},
		},
		ImageRedirectsKey: ImageRedirectTable(nil),
	}
	if _, err := tt.RenderToText(accepted); err != nil {
		t.Fatalf("plain data rejected: %v", err)
	}

	rejected := map[string]struct {
		value interface{}
		want  string
	}{
		"function":          {func() string { return "x" }, "Context.v is a func() string"},
		"pointer":           {&bytes.Buffer{}, "Context.v is a *bytes.Buffer"},
		"named integer":     {time.Second, "Context.v is a time.Duration"},
		"named string":      {namedString("secret-value"), "Context.v is a tmpl.namedString"},
		"non-string keys":   {map[interface{}]interface{}{"k": "v"}, "Context.v is a map[interface {}]interface {}"},
		"typed map":         {map[string]time.Duration{}, "Context.v is a map[string]time.Duration"},
		"in a list":         {[]interface{}{"a", make(chan int)}, "Context.v[1] is a chan int"},
		"in a list of maps": {[]map[string]interface{}{{"k": &bytes.Buffer{}}}, "Context.v[0].k is a *bytes.Buffer"},
	}
	for name, c := range rejected {
		t.Run(name, func(t *testing.T) {
			_, err := tt.RenderToText(map[string]interface{}{"Context": map[string]interface{}{"v": c.value}})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("the error quotes the value: %v", err)
			}
		})
	}
}
