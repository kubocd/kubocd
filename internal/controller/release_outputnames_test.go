package controller

import (
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"strings"
	"testing"
)

func TestEffectiveOutputNameGeneratedAndExplicit(t *testing.T) {
	generated := &kubopackage.OutputRendered{Name: "endpoint", Interface: "trino", Kind: kv1alpha1.KindConnection}
	if got := EffectiveOutputName("trino1", "okdp", generated); got != "kcd-trino1-endpoint" {
		t.Errorf("unexpected generated name: %q", got)
	}
	explicit := &kubopackage.OutputRendered{Name: "oidc", Interface: "oidc", Kind: kv1alpha1.KindConnection, ConnectionName: "keycloak-oidc"}
	if got := EffectiveOutputName("keycloak", "okdp", explicit); got != "keycloak-oidc" {
		t.Errorf("explicit connectionName must win, got %q", got)
	}
	cluster := &kubopackage.OutputRendered{Name: "sso", Interface: "oidc", Kind: kv1alpha1.KindClusterConnection}
	if got := EffectiveOutputName("keycloak", "okdp", cluster); got != "kcd-okdp-keycloak-sso" {
		t.Errorf("unexpected generated cluster name: %q", got)
	}
}

func TestComputeEffectiveOutputNamesDuplicate(t *testing.T) {
	outputs := []*kubopackage.OutputRendered{
		{Name: "a", Interface: "x", Kind: kv1alpha1.KindConnection, ConnectionName: "kcd-rel-b"},
		{Name: "b", Interface: "x", Kind: kv1alpha1.KindConnection},
	}
	// output 'b' generates kcd-rel-b, output 'a' claims it explicitly: clash
	if _, err := ComputeEffectiveOutputNames("rel", "okdp", outputs); err == nil ||
		!strings.Contains(err.Error(), "same connection name") {
		t.Fatalf("expected a duplicate name error, got %v", err)
	}
}

func TestComputeEffectiveOutputNamesInvalid(t *testing.T) {
	outputs := []*kubopackage.OutputRendered{
		{Name: "a", Interface: "x", Kind: kv1alpha1.KindConnection, ConnectionName: "Bad_Name!"},
	}
	if _, err := ComputeEffectiveOutputNames("rel", "okdp", outputs); err == nil ||
		!strings.Contains(err.Error(), "invalid connection name") {
		t.Fatalf("expected an invalid name error, got %v", err)
	}
	long := strings.Repeat("x", 260)
	outputs = []*kubopackage.OutputRendered{
		{Name: "a", Interface: "x", Kind: kv1alpha1.KindConnection, ConnectionName: long},
	}
	if _, err := ComputeEffectiveOutputNames("rel", "okdp", outputs); err == nil {
		t.Fatal("expected a length error")
	}
}
