/*
Copyright 2026 Alexandre MABROUK

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

package kubopackage

import (
	"kubocd/internal/misc"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func renderInputs(t *testing.T, inputs string, parameters string) ([]*InputRendered, error) {
	t.Helper()
	pck := &Package{}
	manifest := "apiVersion: v1alpha1\nname: p\ntag: 1.0.0\nmodules: [{name: main, source: {oci: {repository: r, tag: t}}}]\n" +
		"inputs: " + inputs + "\n"
	if err := misc.YamlDecode(manifest, pck); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if err := pck.Groom(nil); err != nil {
		t.Fatalf("grooming: %v", err)
	}
	params := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(parameters), &params); err != nil {
		t.Fatal(err)
	}
	return pck.RenderInputs(map[string]interface{}{"Parameters": params}, "ns")
}

type inputView struct {
	Alias, Name, Namespace string
	Unbound                bool
	List                   []interface{}
	Item                   []int
}

func view(inputs []*InputRendered) []inputView {
	out := make([]inputView, 0, len(inputs))
	for _, in := range inputs {
		list, item := in.GetList()
		out = append(out, inputView{in.Alias, in.ConnectionRef.Name, in.ConnectionRef.Namespace, in.IsUnbound(), list, item})
	}
	return out
}

func TestFromParameterSingle(t *testing.T) {
	inputs, err := renderInputs(t, "[{contract: database-server, alias: db, fromParameter: metadataDb}]", "metadataDb: pg")
	if err != nil {
		t.Fatal(err)
	}
	if got := view(inputs); !reflect.DeepEqual(got, []inputView{{Alias: "db", Name: "pg", Namespace: "ns"}}) {
		t.Errorf("got %+v", got)
	}
	inputs, err = renderInputs(t, "[{contract: database-server, alias: db, fromParameter: cache, optional: true}]", "{}")
	if err != nil || len(inputs) != 1 || !inputs[0].IsUnbound() {
		t.Errorf("optional and absent: got %+v, %v", view(inputs), err)
	}
	if _, err = renderInputs(t, "[{contract: database-server, fromParameter: db}]", "db: ''"); err == nil {
		t.Error("required and empty: accepted")
	}
}

func TestFromParameterList(t *testing.T) {
	const inputs = "[{contract: s3, alias: storage, fromParameter: 'catalogs[].storage', optional: true}]"
	got, err := renderInputs(t, inputs, "catalogs: [{storage: a}, {name: x}, {storage: c}]")
	if err != nil {
		t.Fatal(err)
	}
	list := []interface{}{nil, nil, nil}
	const alias = "storage"
	want := []inputView{
		{Alias: alias, Unbound: true, List: list},
		{Alias: alias, Name: "a", Namespace: "ns", List: list, Item: []int{0}},
		{Alias: alias, Namespace: "ns", Unbound: true, List: list, Item: []int{1}},
		{Alias: alias, Name: "c", Namespace: "ns", List: list, Item: []int{2}},
	}
	if v := view(got); !reflect.DeepEqual(v, want) {
		t.Errorf("got %+v", v)
	}
	got, err = renderInputs(t, inputs, "{}")
	if err != nil || len(got) != 1 || len(view(got)[0].List) != 0 {
		t.Errorf("no item: got %+v, %v", view(got), err)
	}
	got, err = renderInputs(t, "[{contract: s3, fromParameter: 'groups[].mirrors[]'}]", "groups: [{mirrors: [a, b]}, {mirrors: [c]}]")
	if err != nil || len(got) != 4 || !reflect.DeepEqual(got[3].item, []int{1, 0}) ||
		!reflect.DeepEqual(got[0].list, []interface{}{[]interface{}{nil, nil}, []interface{}{nil}}) {
		t.Errorf("nested: got %+v, %v", view(got), err)
	}
}

func TestFromParameterErrors(t *testing.T) {
	const sa = "s: a"
	cases := map[string]struct{ inputs, parameters, detail string }{
		"required item left empty": {"[{contract: s3, fromParameter: 'c[].s'}]", "c: [{s: a}, {}]", "names no connection"},
		"with connectionRef":       {"[{contract: s3, fromParameter: s, connectionRef: {name: x}}]", sa, "excludes"},
		"with allowMultiple":       {"[{contract: s3, fromParameter: s, allowMultiple: true}]", sa, "excludes"},
		"not a string":             {"[{contract: s3, fromParameter: s}]", "s: {a: b}", "is a string"},
		"not a list":               {"[{contract: s3, fromParameter: 's[]'}]", sa, "expected a list"},
		"not an object":            {"[{contract: s3, fromParameter: 's.t'}]", sa, "expected an object"},
		"bad path":                 {"[{contract: s3, fromParameter: 'a..b'}]", "{}", "joined by dots"},
		"bad brackets":             {"[{contract: s3, fromParameter: 'a[]x'}]", "{}", "joined by dots"},
		"no contract":              {"[{fromParameter: s}]", sa, "contract is required"},
	}
	for name, c := range cases {
		if _, err := renderInputs(t, c.inputs, c.parameters); err == nil || !strings.Contains(err.Error(), c.detail) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestEmptyNameBindsNothing(t *testing.T) {
	const optional = `[{contract: s3, optional: true, connectionRef: {name: "{{ .Parameters.s }}"}}]`
	inputs, err := renderInputs(t, optional, "s: ''")
	if err != nil || len(inputs) != 1 || !inputs[0].IsUnbound() || inputs[0].GetContractLookupNamespace() != "" {
		t.Errorf("optional: got %+v, %v", view(inputs), err)
	}
	if _, err := renderInputs(t, `[{contract: s3, connectionRef: {name: "{{ .Parameters.s }}"}}]`, "s: ''"); err == nil {
		t.Error("required: accepted")
	}
	inputs, err = renderInputs(t, "[{contract: s3, optional: true}]", "{}")
	if err != nil || inputs[0].IsUnbound() || inputs[0].GetContractLookupNamespace() != "ns" {
		t.Errorf("no name at all still looks up by contract: got %+v, %v", view(inputs), err)
	}
}
