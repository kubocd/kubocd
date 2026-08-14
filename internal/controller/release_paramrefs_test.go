package controller

import (
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"strings"
	"testing"
)

func refDecl(path []string, iface string, required bool) kuboschema.ConnectionDecl {
	return kuboschema.ConnectionDecl{Path: path, Interface: iface, Required: required}
}

func TestGenerateScalarRef(t *testing.T) {
	params := map[string]interface{}{"metadataDb": "kcd-postgres-superset"}
	gen, bindings, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"metadataDb"}, "database-server", true)},
		nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Alias != "parameters.metadataDb" ||
		gen[0].NamedConnection.Name != "kcd-postgres-superset" ||
		gen[0].NamedConnection.Namespace != "okdp" || gen[0].Optional {
		t.Fatalf("unexpected generated input: %+v", gen)
	}
	if len(bindings) != 1 || bindings[0].InContext {
		t.Fatalf("unexpected binding: %+v", bindings)
	}
	// Substitution in place, flat values
	inputModel := map[string]interface{}{
		"parameters.metadataDb": map[string]interface{}{"host": "pg.okdp.svc", "port": 5432},
	}
	if err := ApplyRefBindings(bindings, inputModel, params, nil); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	resolved, ok := params["metadataDb"].(map[string]interface{})
	if !ok || resolved["host"] != "pg.okdp.svc" {
		t.Fatalf("in-place substitution failed: %#v", params["metadataDb"])
	}
	if len(inputModel) != 0 {
		t.Errorf("generated aliases must be removed from .Inputs, got %v", inputModel)
	}
}

func TestGenerateRefKindConnection(t *testing.T) {
	params := map[string]interface{}{"metadataDb": "kcd-postgres-superset"}
	decl := refDecl([]string{"metadataDb"}, "database-server", true)
	decl.KindFilter = "Connection"
	gen, _, err := GenerateRefInputs([]kuboschema.ConnectionDecl{decl}, nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Kind != kv1alpha1.KindConnection ||
		gen[0].NamedConnection.Namespace != "okdp" {
		t.Fatalf("unexpected generated input: %+v", gen)
	}
}

func TestGenerateRefKindClusterConnection(t *testing.T) {
	params := map[string]interface{}{"metadataDb": "kcd-shared-postgres"}
	decl := refDecl([]string{"metadataDb"}, "database-server", true)
	decl.KindFilter = "ClusterConnection"
	gen, _, err := GenerateRefInputs([]kuboschema.ConnectionDecl{decl}, nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Kind != kv1alpha1.KindClusterConnection {
		t.Fatalf("unexpected generated input: %+v", gen)
	}
	// Cluster-scoped: no namespace, as the hand-written stanza forbids the pair
	if gen[0].NamedConnection.Namespace != "" {
		t.Errorf("a ClusterConnection ref must not carry a namespace, got '%s'", gen[0].NamedConnection.Namespace)
	}
}

func TestGenerateRefWithoutKind(t *testing.T) {
	params := map[string]interface{}{"metadataDb": "kcd-postgres-superset"}
	gen, _, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"metadataDb"}, "database-server", true)},
		nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Kind != "" || gen[0].NamedConnection.Namespace != "okdp" {
		t.Fatalf("no kind declared: dual lookup and namespace expected, got %+v", gen)
	}
}

func TestGenerateContextRefKind(t *testing.T) {
	context := map[string]interface{}{
		"platform": map[string]interface{}{"oidc": "kcd-keycloak-oidc"},
	}
	decl := refDecl([]string{"platform", "oidc"}, "oidc", true)
	decl.KindFilter = "ClusterConnection"
	gen, _, err := GenerateRefInputs(nil, []kuboschema.ConnectionDecl{decl},
		map[string]interface{}{}, context, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Alias != "context.platform.oidc" ||
		gen[0].Kind != kv1alpha1.KindClusterConnection || gen[0].NamedConnection.Namespace != "" {
		t.Fatalf("unexpected generated input: %+v", gen)
	}
}

func TestGenerateArrayRefs(t *testing.T) {
	params := map[string]interface{}{
		"datasources": []interface{}{
			map[string]interface{}{"name": "trino1", "trino": "kcd-trino1-endpoint", "catalog": "hive"},
			map[string]interface{}{"name": "trino2", "trino": "kcd-trino2-endpoint", "catalog": "raw"},
		},
	}
	gen, bindings, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"datasources", "[]", "trino"}, "trino", true)},
		nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 2 || gen[0].Alias != "parameters.datasources[0].trino" || gen[1].Alias != "parameters.datasources[1].trino" {
		t.Fatalf("unexpected aliases: %+v", gen)
	}
	inputModel := map[string]interface{}{
		"parameters.datasources[0].trino": map[string]interface{}{"internalUri": "trino://t1:8080"},
		"parameters.datasources[1].trino": map[string]interface{}{"internalUri": "trino://t2:8080"},
	}
	if err := ApplyRefBindings(bindings, inputModel, params, nil); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	el1 := params["datasources"].([]interface{})[1].(map[string]interface{})
	trino1 := el1["trino"].(map[string]interface{})
	if trino1["internalUri"] != "trino://t2:8080" {
		t.Errorf("array element substitution failed: %#v", el1)
	}
	if el1["catalog"] != "raw" || el1["name"] != "trino2" {
		t.Errorf("sibling fields must stay intact: %#v", el1)
	}
}

func TestGenerateContextRef(t *testing.T) {
	context := map[string]interface{}{
		"platform": map[string]interface{}{
			"connections": map[string]interface{}{"oidc": "kcd-keycloak-oidc"},
		},
	}
	gen, bindings, err := GenerateRefInputs(nil,
		[]kuboschema.ConnectionDecl{refDecl([]string{"platform", "connections", "oidc"}, "oidc", true)},
		map[string]interface{}{}, context, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || gen[0].Alias != "context.platform.connections.oidc" {
		t.Fatalf("unexpected alias: %+v", gen)
	}
	inputModel := map[string]interface{}{
		"context.platform.connections.oidc": map[string]interface{}{"issuerUri": "http://keycloak/realms/okdp"},
	}
	if err := ApplyRefBindings(bindings, inputModel, map[string]interface{}{}, context); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	oidc := context["platform"].(map[string]interface{})["connections"].(map[string]interface{})["oidc"].(map[string]interface{})
	if oidc["issuerUri"] != "http://keycloak/realms/okdp" {
		t.Errorf("context substitution failed: %#v", oidc)
	}
}

func TestRequiredRefEmptyRejected(t *testing.T) {
	params := map[string]interface{}{"metadataDb": ""}
	if _, _, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"metadataDb"}, "database-server", true)},
		nil, params, nil, "okdp", nil); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("expected a required error, got %v", err)
	}
}

// A required ref whose field is entirely ABSENT (not just empty) must be
// rejected too: that check lives in expandDecl, not in emitDecl.
func TestRequiredRefAbsentRejected(t *testing.T) {
	params := map[string]interface{}{"somethingElse": "x"}
	if _, _, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"metadataDb"}, "database-server", true)},
		nil, params, nil, "okdp", nil); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("expected a required error on an absent field, got %v", err)
	}
}

func TestAliasCollisionWithStanza(t *testing.T) {
	params := map[string]interface{}{"db": "my-db"}
	stanza := &kubopackage.InputRendered{}
	stanza.Interface = "database-server"
	stanza.Alias = "parameters.db"
	if _, _, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"db"}, "database-server", true)},
		nil, params, nil, "okdp", []*kubopackage.InputRendered{stanza}); err == nil ||
		!strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected a collision error, got %v", err)
	}
}

func TestRenderRefDefaults(t *testing.T) {
	decl := kuboschema.ConnectionDecl{
		Path: []string{"oidc"}, Interface: "oidc", Required: true,
		Default: "{{ .Context.platform.defaults.oidc }}",
	}
	params := map[string]interface{}{"oidc": "{{ .Context.platform.defaults.oidc }}"}
	model := map[string]interface{}{
		"Context": map[string]interface{}{
			"platform": map[string]interface{}{"defaults": map[string]interface{}{"oidc": "kcd-keycloak-oidc"}},
		},
	}
	if err := RenderRefDefaults([]kuboschema.ConnectionDecl{decl}, params, model); err != nil {
		t.Fatalf("render defaults failed: %v", err)
	}
	if params["oidc"] != "kcd-keycloak-oidc" {
		t.Errorf("default not rendered: %v", params["oidc"])
	}
}
