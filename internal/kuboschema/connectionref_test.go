package kuboschema

import (
	"strings"
	"testing"
)

func TestDesugarConnectionRefScalar(t *testing.T) {
	schema := KuboSchema{
		"properties": map[string]interface{}{
			"metadataDb": map[string]interface{}{
				"type":      TypeConnectionRef,
				"interface": "database-server",
				"required":  true,
			},
		},
	}
	openAPI, err := Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	props := openAPI["properties"].(map[string]interface{})
	node := props["metadataDb"].(map[string]interface{})
	if node["type"] != "string" {
		t.Errorf("de-sugared type should be string, got %v", node["type"])
	}
	marker, ok := node[MarkerConnectionRef].(map[string]interface{})
	if !ok || marker["interface"] != "database-server" {
		t.Errorf("marker missing or wrong: %v", node)
	}
	decls, err := CollectConnectionDecls(openAPI, false)
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	if len(decls) != 1 || decls[0].Interface != "database-server" || !decls[0].Required || decls[0].Selector {
		t.Errorf("unexpected decl: %+v", decls)
	}
}

func TestDesugarConnectionRefLiteralDefaultRejected(t *testing.T) {
	schema := KuboSchema{
		"properties": map[string]interface{}{
			"db": map[string]interface{}{
				"type":      TypeConnectionRef,
				"interface": "database-server",
				"default":   "my-hardcoded-connection",
			},
		},
	}
	if _, err := Kubo2openAPI(schema, false); err == nil ||
		!strings.Contains(err.Error(), "template string") {
		t.Fatalf("expected a literal default rejection, got %v", err)
	}
}

func TestDesugarConnectionRefInArray(t *testing.T) {
	schema := KuboSchema{
		"properties": map[string]interface{}{
			"datasources": map[string]interface{}{
				"items": map[string]interface{}{
					"properties": map[string]interface{}{
						"trino":   map[string]interface{}{"type": TypeConnectionRef, "interface": "trino", "required": true},
						"catalog": map[string]interface{}{"type": "string", "default": "hive"},
					},
				},
			},
		},
	}
	openAPI, err := Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	decls, err := CollectConnectionDecls(openAPI, false)
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl, got %+v", decls)
	}
	want := []string{"datasources", "[]", "trino"}
	if len(decls[0].Path) != 3 || decls[0].Path[0] != want[0] || decls[0].Path[1] != want[1] || decls[0].Path[2] != want[2] {
		t.Errorf("unexpected path: %v", decls[0].Path)
	}
	if !decls[0].Required {
		t.Errorf("item-level required should be carried")
	}
}

func TestDesugarConnectionSelector(t *testing.T) {
	schema := KuboSchema{
		"properties": map[string]interface{}{
			"databases": map[string]interface{}{
				"type":        TypeConnectionSelector,
				"interface":   "database-server",
				"matchLabels": map[string]interface{}{"backup": "enabled"},
				"required":    true,
			},
		},
	}
	openAPI, err := Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	// The selector must never be required at the openAPI level
	if reqList, ok := openAPI["required"].([]string); ok {
		for _, r := range reqList {
			if r == "databases" {
				t.Errorf("selector must not be required in the openAPI schema")
			}
		}
	}
	decls, err := CollectConnectionDecls(openAPI, false)
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	if len(decls) != 1 || !decls[0].Selector || !decls[0].Required || decls[0].MatchLabels["backup"] != "enabled" {
		t.Errorf("unexpected decl: %+v", decls)
	}
}

func TestSelectorRejectedInArrayAndContext(t *testing.T) {
	inArray := KuboSchema{
		"properties": map[string]interface{}{
			"list": map[string]interface{}{
				"items": map[string]interface{}{
					"properties": map[string]interface{}{
						"sel": map[string]interface{}{"type": TypeConnectionSelector, "interface": "s3"},
					},
				},
			},
		},
	}
	openAPI, err := Kubo2openAPI(inArray, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	if _, err := CollectConnectionDecls(openAPI, false); err == nil ||
		!strings.Contains(err.Error(), "arrays") {
		t.Fatalf("expected an in-array rejection, got %v", err)
	}
	inContext := KuboSchema{
		"properties": map[string]interface{}{
			"sel": map[string]interface{}{"type": TypeConnectionSelector, "interface": "s3"},
		},
	}
	openAPI, err = Kubo2openAPI(inContext, true)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	if _, err := CollectConnectionDecls(openAPI, true); err == nil ||
		!strings.Contains(err.Error(), "schema.context") {
		t.Fatalf("expected a context rejection, got %v", err)
	}
}

func TestRefDefaultRejectedInContext(t *testing.T) {
	schema := KuboSchema{
		"properties": map[string]interface{}{
			"oidc": map[string]interface{}{
				"type":      TypeConnectionRef,
				"interface": "oidc",
				"default":   "{{ .Context.platform.defaults.oidc }}",
			},
		},
	}
	openAPI, err := Kubo2openAPI(schema, true)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	if _, err := CollectConnectionDecls(openAPI, true); err == nil ||
		!strings.Contains(err.Error(), "cannot have a default") {
		t.Fatalf("expected a context default rejection, got %v", err)
	}
}
