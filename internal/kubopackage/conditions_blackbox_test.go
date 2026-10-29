//nolint:goconst // Keep policy inputs and expected results together.
package kubopackage

import (
	"fmt"
	kv1 "kubocd/api/v1alpha1"
	"kubocd/internal/tmpl"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

const scopePolicyMessage = "only core Secret and ConfigMap conditions in the Release target namespace are allowed"

func renderScopeConditions(t *testing.T, conditions, target string) (*Rendered, error) {
	t.Helper()
	manifest := `apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    helmRepository:
      url: https://example.com/charts
      chart: demo
      version: 1.0.0
conditions: |
` + "  " + strings.ReplaceAll(conditions, "\n", "\n  ") + "\n"
	p := &Package{}
	if err := yaml.Unmarshal([]byte(manifest), p); err != nil {
		t.Fatal(err)
	}
	if err := p.Groom(nil, tmpl.PackageFunctions(false)); err != nil {
		t.Fatal(err)
	}
	release := &kv1.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "control"}, Spec: kv1.ReleaseSpec{TargetNamespace: target}}
	model := map[string]interface{}{"Release": map[string]interface{}{"spec": map[string]interface{}{"targetNamespace": target, "createNamespace": false}}}
	return p.Render(model, release)
}

func TestPackageConditionScope(t *testing.T) {
	for _, tc := range []struct{ name, fields string }{
		{"Lease", "group: coordination.k8s.io\nkind: Lease"},
		{"Connection", "group: kubocd.kubotal.io\nkind: Connection"},
		{"CRD", "group: apiextensions.k8s.io\nkind: CustomResourceDefinition"},
		{"unknown CR", "group: example.invalid\nkind: Widget"},
		{"cluster scoped", "kind: Namespace"},
		{"cluster connection", "group: kubocd.kubotal.io\nkind: ClusterConnection"},
		{"other group", "group: core-canary\nkind: Secret"},
		{"v1 is not a group", "group: v1\nkind: ConfigMap"},
		{"foreign namespace", "kind: Secret\nnamespace: foreign-canary"},
		{"metadata is not target", "kind: ConfigMap\nnamespace: control"},
		{"plural", "kind: secrets"},
		{"non-ASCII kind", "kind: CONFİGMAP"},
		{"whitespace", "kind: ' Secret'"},
		{"missing kind", "group: ''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			condition := "- name: name-canary\n  cel: \"'expression-canary' == 'expression-canary'\"\n  " + strings.ReplaceAll(tc.fields, "\n", "\n  ")
			for _, prefix := range []string{"", "- kind: Secret\n  name: absent\n  cel: 'false'\n"} {
				_, err := renderScopeConditions(t, prefix+condition, "target")
				index := 0
				if prefix != "" {
					index = 1
				}
				want := fmt.Sprintf("condition[%d]: %s", index, scopePolicyMessage)
				if err == nil || err.Error() != want {
					t.Errorf("got %v, want %q", err, want)
				}
			}
		})
	}
	for _, tc := range []struct{ kind, canonical string }{
		{"secret", "Secret"}, {"Secret", "Secret"}, {"SECRET", "Secret"},
		{"configmap", "ConfigMap"}, {"ConfigMap", "ConfigMap"}, {"CONFIGMAP", "ConfigMap"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			for _, target := range []string{"target", ""} {
				wantNS := target
				if wantNS == "" {
					wantNS = "control"
				}
				for _, explicit := range []string{"", "\n  namespace: " + wantNS} {
					rendered, err := renderScopeConditions(t, "- kind: "+tc.kind+"\n  name: db"+explicit, target)
					if err != nil {
						t.Fatal(err)
					}
					got := rendered.Conditions[0]
					if string(got.Kind) != tc.canonical || got.Namespace != wantNS || got.Group != "" {
						t.Errorf("got %+v, want core %s in %s", got, tc.canonical, wantNS)
					}
				}
			}
		})
	}
}
