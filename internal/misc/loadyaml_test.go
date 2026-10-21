/*
Copyright 2026 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package misc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var literalSequences = map[string]interface{}{
	"set":     "${SEC1_PROBE}",
	"unset":   "${SEC1_UNSET_PROBE}",
	"home":    "prefix-${HOME}",
	"colon":   "${ENV:x}",
	"pattern": "${BUCKET#s3://}",
}

func writeFile(t *testing.T, name string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadYamlKeepsDollarSequencesLiteral(t *testing.T) {
	t.Setenv("SEC1_PROBE", "from-the-environment")
	path := writeFile(t, "manifest.yaml", `
set: ${SEC1_PROBE}
unset: ${SEC1_UNSET_PROBE}
home: prefix-${HOME}
colon: ${ENV:x}
pattern: ${BUCKET#s3://}
`)
	first, second := map[string]interface{}{}, map[string]interface{}{}
	if err := LoadYaml(path, &first, &second); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	for _, got := range []map[string]interface{}{first, second} {
		if !reflect.DeepEqual(got, literalSequences) {
			t.Errorf("got %v, want %v", got, literalSequences)
		}
	}
}

func TestLoadJsonKeepsDollarSequencesLiteral(t *testing.T) {
	t.Setenv("SEC1_PROBE", "from-the-environment")
	path := writeFile(t, "index.json", `{"set":"${SEC1_PROBE}","unset":"${SEC1_UNSET_PROBE}",`+
		`"home":"prefix-${HOME}","colon":"${ENV:x}","pattern":"${BUCKET#s3://}"}`)
	got := map[string]interface{}{}
	if err := LoadJson(path, &got); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if !reflect.DeepEqual(got, literalSequences) {
		t.Errorf("got %v, want %v", got, literalSequences)
	}
}
