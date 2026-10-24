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

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The registry address is closed, and the credentials come from the
// environment, so that no credential helper runs.
func TestAPushWithoutManifestJSONNamesIt(t *testing.T) {
	assembly := t.TempDir()
	if err := os.WriteFile(filepath.Join(assembly, "assembly.tgz"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KCD_OCI_127_0_0_1_1_USER", "user")
	t.Setenv("KCD_OCI_127_0_0_1_1_SECRET", "secret")
	err := pushImage(assembly, "127.0.0.1:1/kubocd/packages/demo", "0.1.0", true)
	want := "failed to add manifest.json to the OCI file system: "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("got %v, want an error that starts with %q", err, want)
	}
}
