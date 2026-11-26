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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

func TestRenderRefusesAReleaseWithADurationOutOfRange(t *testing.T) {
	if base := os.Getenv("KUBOCD_RELEASE_DURATION_PROBE"); base != "" {
		renderParams.output = filepath.Join(base, "output")
		renderParams.workDir = filepath.Join(base, "work")
		renderLog = logr.Discard()
		renderCmd.Run(renderCmd, []string{filepath.Join(base, "release.yaml")})
		return
	}
	base := t.TempDir()
	release := "apiVersion: kubocd.kubotal.io/v1alpha1\nkind: Release\nmetadata:\n  name: demo\nspec:\n  package:\n" +
		"    repository: quay.io/kubocd/packages/demo\n    tag: 0.1.0\n    interval: 9999999h\n"
	if err := os.WriteFile(filepath.Join(base, "release.yaml"), []byte(release), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRenderRefusesAReleaseWithADurationOutOfRange$")
	command.Env = append(os.Environ(), "KUBOCD_RELEASE_DURATION_PROBE="+base, "KUBECONFIG="+filepath.Join(base, "missing"),
		"KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("the Release was accepted: %s", output)
	}
	want := `invalid release: spec.package.interval: "9999999h" is not a duration of at most about 2562047h, ` +
		`such as 90s or 2m30s`
	if !strings.Contains(string(output), want) {
		t.Fatalf("got %s, want %q", output, want)
	}
	if _, statErr := os.Stat(filepath.Join(base, "output")); !os.IsNotExist(statErr) {
		t.Fatalf("got the output folder (%v), want none", statErr)
	}
}
