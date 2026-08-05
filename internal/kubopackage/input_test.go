package kubopackage

import (
	"strings"
	"testing"
)

func groomedInput(t *testing.T, i *Input) *Input {
	t.Helper()
	if err := i.groom(&Package{}); err != nil {
		t.Fatalf("groom failed: %v", err)
	}
	return i
}

// A named connection without explicit kind is looked up as Connection AND
// ClusterConnection: the Connection side needs the default namespace.
func TestInputRenderNamedConnectionNamespaceDefaultDualKind(t *testing.T) {
	i := groomedInput(t, func() *Input {
		i := &Input{Interface: "database-server"}
		i.NamedConnection.Name = "my-db"
		return i
	}())
	ir, err := i.Render(map[string]interface{}{}, "okdp")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if ir.NamedConnection.Namespace != "okdp" {
		t.Errorf("namespace should default to release namespace for dual-kind lookup, got %q", ir.NamedConnection.Namespace)
	}
}

func TestInputRenderNamedClusterConnectionNoNamespace(t *testing.T) {
	i := groomedInput(t, func() *Input {
		i := &Input{Interface: "oidc", Kind: "ClusterConnection"}
		i.NamedConnection.Name = "sso"
		return i
	}())
	ir, err := i.Render(map[string]interface{}{}, "okdp")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if ir.NamedConnection.Namespace != "" {
		t.Errorf("namespace must stay empty for an explicit ClusterConnection, got %q", ir.NamedConnection.Namespace)
	}
}

func TestInputRenderNamespaceOnClusterConnectionRejected(t *testing.T) {
	i := groomedInput(t, func() *Input {
		i := &Input{Interface: "oidc", Kind: "ClusterConnection"}
		i.NamedConnection.Name = "sso"
		i.NamedConnection.Namespace = "somewhere"
		return i
	}())
	if _, err := i.Render(map[string]interface{}{}, "okdp"); err == nil ||
		!strings.Contains(err.Error(), "namespace must be empty") {
		t.Fatalf("expected a namespace rejection error, got %v", err)
	}
}

func TestInputRenderAliasDefaultsToInterface(t *testing.T) {
	i := groomedInput(t, &Input{Interface: "s3"})
	ir, err := i.Render(map[string]interface{}{}, "okdp")
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if ir.Alias != "s3" {
		t.Errorf("alias should default to interface, got %q", ir.Alias)
	}
	if ir.InterfaceLookup.Namespace != "okdp" {
		t.Errorf("lookup namespace should default to release namespace, got %q", ir.InterfaceLookup.Namespace)
	}
}
