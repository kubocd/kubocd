package controller

import (
	kv1alpha1 "kubocd/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestOwnedByRelease(t *testing.T) {
	c := &kv1alpha1.Connection{}
	if ownedByRelease(c, "rel") {
		t.Error("no owner: must not be owned")
	}
	isController := true
	c.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: kv1alpha1.GroupVersion.String(), Kind: "Release",
		Name: "other", Controller: &isController,
	}}
	if ownedByRelease(c, "rel") {
		t.Error("owned by another release: must not match")
	}
	c.OwnerReferences[0].Name = "rel"
	if !ownedByRelease(c, "rel") {
		t.Error("owned by this release: must match")
	}
}

func TestApplyManagedLabelsConvergence(t *testing.T) {
	meta := &metav1.ObjectMeta{Labels: map[string]string{"foreign": "keep"}}
	ApplyManagedLabels(meta, map[string]string{"backup": "enabled", "tier": "prod"})
	if meta.Labels["backup"] != "enabled" || meta.Labels["tier"] != "prod" || meta.Labels["foreign"] != "keep" {
		t.Fatalf("apply failed: %v", meta.Labels)
	}
	// The package drops 'backup': it must be removed, 'foreign' preserved
	ApplyManagedLabels(meta, map[string]string{"tier": "prod"})
	if _, still := meta.Labels["backup"]; still {
		t.Errorf("stale applied label must be removed: %v", meta.Labels)
	}
	if meta.Labels["foreign"] != "keep" {
		t.Errorf("foreign label must be preserved: %v", meta.Labels)
	}
	// All labels dropped: annotation cleaned
	ApplyManagedLabels(meta, nil)
	if _, still := meta.Labels["tier"]; still {
		t.Errorf("all applied labels must be removed: %v", meta.Labels)
	}
	if _, still := meta.Annotations[AppliedLabelsAnnotation]; still {
		t.Errorf("tracking annotation must be cleaned: %v", meta.Annotations)
	}
}
