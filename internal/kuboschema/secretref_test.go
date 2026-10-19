package kuboschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func secretRefSchema(t *testing.T, src string) KuboSchema {
	schema := KuboSchema{}
	if err := json.Unmarshal([]byte(src), &schema); err != nil {
		t.Fatalf("invalid test schema: %v", err)
	}
	return schema
}

func TestDesugarSecretRef(t *testing.T) {
	schema := secretRefSchema(t, `{"properties": {"db": {"type": "secretRef", "keys": ["username", "password"], "required": true}}}`)
	openAPI, err := Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	node := openAPI["properties"].(map[string]interface{})["db"].(map[string]interface{})
	if node[keyType] != typeString {
		t.Errorf("de-sugared type should be string, got %v", node[keyType])
	}
	if _, ok := node[keyKeys]; ok {
		t.Errorf("'keys' must not remain in the openAPI node: %v", node)
	}
	decls := CollectSecretRefDecls(openAPI)
	if len(decls) != 1 || !decls[0].Required ||
		!reflect.DeepEqual(decls[0].Path, []string{"db"}) ||
		!reflect.DeepEqual(decls[0].Keys, []string{"username", "password"}) {
		t.Errorf("unexpected decls: %+v", decls)
	}
}

func TestDesugarSecretRefInArray(t *testing.T) {
	schema := secretRefSchema(t, `{"properties": {"buckets": {"items": {"properties": {
		"credentials": {"type": "secretRef", "keys": ["accessKey", "secretKey"]}}}}}}`)
	openAPI, err := Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	decls := CollectSecretRefDecls(openAPI)
	if len(decls) != 1 || decls[0].Required ||
		!reflect.DeepEqual(decls[0].Path, []string{"buckets", "[]", "credentials"}) {
		t.Errorf("unexpected decls: %+v", decls)
	}
}

func TestDesugarSecretRefInvalid(t *testing.T) {
	cases := map[string]string{
		"missing keys":     `{"type": "secretRef"}`,
		"empty keys":       `{"type": "secretRef", "keys": []}`,
		"not a list":       `{"type": "secretRef", "keys": "username"}`,
		"non-string key":   `{"type": "secretRef", "keys": ["username", 1]}`,
		"empty key":        `{"type": "secretRef", "keys": [""]}`,
		"duplicate key":    `{"type": "secretRef", "keys": ["username", "username"]}`,
		"non-string value": `{"type": "secretRef", "keys": ["username"], "default": 1}`,
		"unknown property": `{"type": "secretRef", "keys": ["username"], "contract": "x"}`,
	}
	for name, node := range cases {
		schema := secretRefSchema(t, `{"properties": {"db": `+node+`}}`)
		if _, err := Kubo2openAPI(schema, false); err == nil || !strings.Contains(err.Error(), "db") {
			t.Errorf("%s: expected an error on node 'db', got %v", name, err)
		}
	}
}
