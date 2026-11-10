//nolint:goconst // test inputs read better inline
package kuboschema

import (
	"reflect"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

func claimSchema(node map[string]interface{}) KuboSchema {
	return KuboSchema{"properties": map[string]interface{}{
		"db":       node,
		"replicas": map[string]interface{}{"type": "integer"},
	}}
}

func TestDesugarClaim(t *testing.T) {
	requirements := map[string]interface{}{"engines": []interface{}{"postgresql"}, "serverVersion": ">=15"}
	openAPI, err := Kubo2openAPI(claimSchema(map[string]interface{}{
		"type": TypeClaim, "contract": "database-v1", "required": true, "requirements": requirements, "description": "Metastore",
	}), false)
	if err != nil {
		t.Fatal(err)
	}
	decls, err := CollectClaimDecls(openAPI, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []ClaimDecl{{Path: []string{"db"}, Contract: "database-v1", Required: true, Requirements: requirements}}
	if !reflect.DeepEqual(decls, want) {
		t.Fatalf("got %+v, want %+v", decls, want)
	}
	schema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(openAPI))
	if err != nil {
		t.Fatal(err)
	}
	for parameters, valid := range map[string]bool{
		`{"db": {"provisioner": "postgresql"}}`:                             true,
		`{"db": {"connection": "ext-pg"}}`:                                  true,
		`{"db": {"provisioner": "pg", "requirements": {"dbName": "hive"}}}`: true,
		`{"db": {"requirements": {"dbName": "hive"}}}`:                      false,
		`{"db": {"provisioner": "pg", "requirements": "hive"}}`:             false,
		`{"db": {}}`: false,
		`{"db": {"provisioner": "postgresql", "connection": "x"}}`: false,
		`{"db": {"provisioner": ""}}`:                              false,
		`{"db": {"storageClass": "fast"}}`:                         false,
		`{"db": "ext-pg"}`:                                         false,
		`{"replicas": 1}`:                                          false,
	} {
		result, err := schema.Validate(gojsonschema.NewStringLoader(parameters))
		if err != nil || result.Valid() != valid {
			t.Errorf("%s: got valid %v (%v), want %v", parameters, result.Valid(), err, valid)
		}
	}
}

func TestAClaimMarkerInAnOpenAPISchemaGetsTheClaimShape(t *testing.T) {
	marker := map[string]interface{}{"contract": "database-v1", "requirements": map[string]interface{}{"engines": []interface{}{"postgresql"}}}
	openAPI, err := Kubo2openAPI(KuboSchema{
		"$schema":  "http://json-schema.org/schema#",
		"type":     "object",
		"required": []interface{}{"db"},
		"properties": map[string]interface{}{
			"db": map[string]interface{}{"type": "object", "title": "Metastore", MarkerClaim: marker},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	decls, err := CollectClaimDecls(openAPI, false)
	want := []ClaimDecl{{Path: []string{"db"}, Contract: "database-v1", Required: true, Requirements: marker["requirements"].(map[string]interface{})}}
	if err != nil || !reflect.DeepEqual(decls, want) {
		t.Fatalf("got %+v, %v", decls, err)
	}
	sugared, err := Kubo2openAPI(claimSchema(map[string]interface{}{
		"type": TypeClaim, "contract": "database-v1", "required": true, "title": "Metastore", "requirements": marker["requirements"],
	}), false)
	if err != nil {
		t.Fatal(err)
	}
	db := openAPI["properties"].(map[string]interface{})["db"]
	if sugaredDB := sugared["properties"].(map[string]interface{})["db"]; !reflect.DeepEqual(db, sugaredDB) {
		t.Errorf("the marker gives %v, the claim type gives %v", db, sugaredDB)
	}

	if _, err := CollectClaimDecls(map[string]interface{}{"properties": map[string]interface{}{
		"db": map[string]interface{}{MarkerClaim: map[string]interface{}{}},
	}}, false); err == nil || !strings.Contains(err.Error(), "names no contract") {
		t.Errorf("a marker without contract: got %v", err)
	}
}

func TestDesugarClaimRejections(t *testing.T) {
	for name, c := range map[string]struct {
		node map[string]interface{}
		want string
	}{
		"no contract":  {map[string]interface{}{"type": TypeClaim}, "'contract' is required"},
		"default":      {map[string]interface{}{"type": TypeClaim, "contract": "database-v1", "default": map[string]interface{}{}}, "unknown property 'default'"},
		"requirements": {map[string]interface{}{"type": TypeClaim, "contract": "database-v1", "requirements": ">=15"}, "'requirements' must be a map"},
	} {
		if _, err := Kubo2openAPI(claimSchema(c.node), false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, c.want)
		}
	}
}

func TestCollectClaimDeclsPositions(t *testing.T) {
	claim := map[string]interface{}{"type": TypeClaim, "contract": "database-v1"}
	nested := KuboSchema{"properties": map[string]interface{}{
		"metadata": map[string]interface{}{"properties": map[string]interface{}{"db": claim}},
		"examples": map[string]interface{}{"type": TypeClaim, "contract": "database-v1"},
	}}
	openAPI, err := Kubo2openAPI(nested, false)
	if err != nil {
		t.Fatal(err)
	}
	decls, err := CollectClaimDecls(openAPI, false)
	if err != nil || len(decls) != 2 || !reflect.DeepEqual(decls[0].Path, []string{"examples"}) ||
		!reflect.DeepEqual(decls[1].Path, []string{"metadata", "db"}) || decls[1].Required {
		t.Fatalf("got %+v, %v", decls, err)
	}
	if _, err := CollectClaimDecls(openAPI, true); err == nil || !strings.Contains(err.Error(), "not allowed in schema.context") {
		t.Errorf("in the context: got %v", err)
	}
	inArray, err := Kubo2openAPI(KuboSchema{"properties": map[string]interface{}{
		"datasources": map[string]interface{}{"items": map[string]interface{}{"properties": map[string]interface{}{"db": claim}}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectClaimDecls(inArray, false); err == nil || !strings.Contains(err.Error(), "inside array items") {
		t.Errorf("in array items: got %v", err)
	}
}
