package controller

import (
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
	if len(bindings) != 1 || bindings[0].InContext || bindings[0].List {
		t.Fatalf("unexpected binding: %+v", bindings)
	}
	// Substitution in place, flat values
	inputModel := map[string]interface{}{
		"parameters.metadataDb": map[string]interface{}{"host": "pg.okdp.svc", "port": 5432},
	}
	if err := ApplyRefBindings(bindings, inputModel, map[string]interface{}{}, params, nil); err != nil {
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
	if err := ApplyRefBindings(bindings, inputModel, map[string]interface{}{}, params, nil); err != nil {
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
	if err := ApplyRefBindings(bindings, inputModel, map[string]interface{}{}, map[string]interface{}{}, context); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	oidc := context["platform"].(map[string]interface{})["connections"].(map[string]interface{})["oidc"].(map[string]interface{})
	if oidc["issuerUri"] != "http://keycloak/realms/okdp" {
		t.Errorf("context substitution failed: %#v", oidc)
	}
}

func TestGenerateSelector(t *testing.T) {
	params := map[string]interface{}{}
	decl := kuboschema.ConnectionDecl{
		Path: []string{"databases"}, Interface: "database-server",
		Selector: true, Required: false,
		MatchLabels: map[string]string{"backup": "enabled"},
	}
	gen, bindings, err := GenerateRefInputs([]kuboschema.ConnectionDecl{decl}, nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(gen) != 1 || !gen[0].AllowMultiple || !gen[0].Optional ||
		gen[0].MatchLabels["backup"] != "enabled" || gen[0].InterfaceLookup.Namespace != "okdp" {
		t.Fatalf("unexpected generated input: %+v", gen)
	}
	// Optional selector with no match: empty list substituted
	if err := ApplyRefBindings(bindings, map[string]interface{}{}, map[string]interface{}{}, params, nil); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	list, ok := params["databases"].([]map[string]interface{})
	if !ok || len(list) != 0 {
		t.Fatalf("expected an empty resolved list, got %#v", params["databases"])
	}
}

func TestSelectorSetByDeployerRejected(t *testing.T) {
	params := map[string]interface{}{"databases": "nope"}
	decl := kuboschema.ConnectionDecl{Path: []string{"databases"}, Interface: "database-server", Selector: true}
	if _, _, err := GenerateRefInputs([]kuboschema.ConnectionDecl{decl}, nil, params, nil, "okdp", nil); err == nil ||
		!strings.Contains(err.Error(), "cannot be set by the release") {
		t.Fatalf("expected a rejection, got %v", err)
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

func TestAliasCollisionWithStanza(t *testing.T) {
	params := map[string]interface{}{"db": "my-db"}
	var stanza kubopackage.InputRendered
	stanza.Interface = "database-server"
	stanza.Alias = "parameters.db"
	if _, _, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{refDecl([]string{"db"}, "database-server", true)},
		nil, params, nil, "okdp", []kubopackage.InputRendered{stanza}); err == nil ||
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

func TestMatchesLabelsFilter(t *testing.T) {
	if !matchesLabels(map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1"}) {
		t.Error("subset should match")
	}
	if matchesLabels(map[string]string{"a": "1"}, map[string]string{"a": "2"}) {
		t.Error("wrong value should not match")
	}
	if !matchesLabels(nil, nil) {
		t.Error("empty filter should match everything")
	}
}
