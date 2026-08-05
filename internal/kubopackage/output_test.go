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
