package kubopackage

import (
	"testing"
)

func groomedOutput(t *testing.T, o *Output) *Output {
	t.Helper()
	if err := o.groom(&Package{}); err != nil {
		t.Fatalf("groom failed: %v", err)
	}
	return o
}

func TestOutputRenderDefaults(t *testing.T) {
	o := groomedOutput(t, &Output{Interface: "database-server"})
	or, err := o.Render(map[string]interface{}{})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if or.Name != "database-server" {
		t.Errorf("name should default to interface, got %q", or.Name)
	}
	if or.Kind != "Connection" {
		t.Errorf("kind should default to Connection, got %q", or.Kind)
	}
	if or.ConnectionName != "" {
		t.Errorf("connectionName should default to empty, got %q", or.ConnectionName)
	}
	if or.Priority != 100 {
		t.Errorf("priority should default to 100, got %d", or.Priority)
	}
}

func TestOutputRenderConnectionName(t *testing.T) {
	o := groomedOutput(t, &Output{
		Interface:      "oidc",
		Name:           "oidc",
		ConnectionName: "{{ .Static }}-oidc",
	})
	or, err := o.Render(map[string]interface{}{"Static": "keycloak"})
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if or.ConnectionName != "keycloak-oidc" {
		t.Errorf("expected templated connectionName 'keycloak-oidc', got %q", or.ConnectionName)
	}
}

func TestOutputRenderBadKind(t *testing.T) {
	o := groomedOutput(t, &Output{Interface: "x", Kind: "Nimp"})
	if _, err := o.Render(map[string]interface{}{}); err == nil {
		t.Fatal("expected an error on invalid kind")
	}
}

func TestOutputsStanzaUnmarshalBothForms(t *testing.T) {
	var s1 OutputsStanza
	if err := s1.UnmarshalJSON([]byte(`"{{ .X }}"`)); err != nil || s1.Template == "" {
		t.Fatalf("string form should fill Template, err=%v", err)
	}
	var s2 OutputsStanza
	if err := s2.UnmarshalJSON([]byte(`[{"interface":"s3"}]`)); err != nil || len(s2.List) != 1 {
		t.Fatalf("list form should fill List, err=%v", err)
	}
}

func TestOutputsStanzaBlockTemplate(t *testing.T) {
	os := OutputsStanza{Template: `
{{- range .Parameters.databases }}
- name: "{{ .name }}"
  interface: database-server
  labels: { backup: enabled }
  values:
    dbName: "{{ .name }}"
    secretRef: "{{ .secret }}"
{{- end }}`}
	if err := os.groom(&Package{}); err != nil {
		t.Fatalf("groom failed: %v", err)
	}
	model := map[string]interface{}{
		"Parameters": map[string]interface{}{
			"databases": []interface{}{
				map[string]interface{}{"name": "keycloak", "secret": "pg-keycloak"},
				map[string]interface{}{"name": "superset", "secret": "pg-superset"},
			},
		},
	}
	outs, err := os.Render(model)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if len(outs) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(outs))
	}
	if outs[0].Name != "keycloak" || outs[1].Name != "superset" {
		t.Errorf("unexpected names: %s, %s", outs[0].Name, outs[1].Name)
	}
	if outs[0].Kind != "Connection" || outs[0].Priority != 100 {
		t.Errorf("defaults not applied: kind=%s priority=%d", outs[0].Kind, outs[0].Priority)
	}
	if outs[0].Labels["backup"] != "enabled" {
		t.Errorf("labels not carried: %v", outs[0].Labels)
	}
	if outs[1].Values["secretRef"] != "pg-superset" {
		t.Errorf("values not rendered: %v", outs[1].Values)
	}
}

func TestOutputsStanzaBlockTemplateEmpty(t *testing.T) {
	os := OutputsStanza{Template: `{{- if false }}- interface: x{{ end }}`}
	if err := os.groom(&Package{}); err != nil {
		t.Fatalf("groom failed: %v", err)
	}
	outs, err := os.Render(map[string]interface{}{})
	if err != nil || len(outs) != 0 {
		t.Fatalf("empty rendering should give 0 outputs, got %d (err=%v)", len(outs), err)
	}
}

func TestOutputRenderBadLabelValue(t *testing.T) {
	o := groomedOutput(t, &Output{Interface: "x", Labels: map[string]interface{}{"tier": "not valid!"}})
	if _, err := o.Render(map[string]interface{}{}); err == nil {
		t.Fatal("expected an invalid label value error")
	}
}

func TestOutputsStanzaListForm(t *testing.T) {
	os := OutputsStanza{List: []Output{{Interface: "s3", Labels: map[string]interface{}{"tier": "prod"}}}}
	if err := os.groom(&Package{}); err != nil {
		t.Fatalf("groom failed: %v", err)
	}
	outs, err := os.Render(map[string]interface{}{})
	if err != nil || len(outs) != 1 {
		t.Fatalf("expected 1 output, err=%v", err)
	}
	if outs[0].Labels["tier"] != "prod" {
		t.Errorf("labels not rendered on list form: %v", outs[0].Labels)
	}
}

func TestOutputsStanzaListFormStrict(t *testing.T) {
	var s OutputsStanza
	if err := s.UnmarshalJSON([]byte(`[{"interface":"s3","lables":{"tier":"prod"}}]`)); err == nil {
		t.Fatal("an unknown field in an output entry must be rejected")
	}
}
