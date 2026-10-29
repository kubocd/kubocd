//nolint:goconst // Keep command fixtures beside their assertions.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderRejectsForbiddenConditions(t *testing.T) {
	base := t.TempDir()
	write := func(name, data string) string {
		t.Helper()
		path := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("chart/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
	write("bin/helm", "#!/bin/sh\nexit 0\n")
	release := write("release.yaml", `apiVersion: kubocd.kubotal.io/v1alpha1
kind: Release
metadata:
  name: demo
  namespace: control
spec:
  targetNamespace: target
  package:
    repository: example.invalid/demo
    tag: 0.1.0
`)
	config := write("kubeconfig", `apiVersion: v1
kind: Config
clusters:
- name: fixture
  cluster:
    server: http://fixture.invalid
contexts:
- name: fixture
  context:
    cluster: fixture
    user: fixture
current-context: fixture
users:
- name: fixture
  user: {}
`)
	for _, condition := range []string{
		"group: coordination.k8s.io\n    kind: Lease", "kind: Secret\n    namespace: foreign-canary",
	} {
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skip=%t", condition, skip), func(t *testing.T) {
				manifest := write("package.yaml", fmt.Sprintf(`apiVersion: v1alpha1
name: demo
tag: 0.1.0
conditions: |
  - kind: Secret
    name: absent
    cel: 'false'
  - %s
    name: name-canary
modules:
- name: main
  source:
    local:
      path: %q
`, condition, filepath.Join(base, "chart")))
				args := []string{"render", release, manifest, "--output=" + filepath.Join(t.TempDir(), "out"),
					"--workDir", t.TempDir(), fmt.Sprintf("--skipConditions=%t", skip)}
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChartSourceCommandProcess$")
				child.Env = append(os.Environ(), "KUBOCD_SOURCE_TEST_ARGS="+string(encoded), "KUBECONFIG="+config,
					"KUBERNETES_SERVICE_HOST=", "PATH="+filepath.Join(base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
				output, runErr := child.CombinedOutput()
				want := "could not render package: condition[1]: " +
					"only core Secret and ConfigMap conditions in the Release target namespace are allowed"
				if runErr == nil || !strings.Contains(string(output), want) {
					t.Fatalf("got %v\n%s\nwant %s", runErr, output, want)
				}
				if strings.Contains(string(output), "canary") || strings.Contains(string(output), "Check condition") {
					t.Fatalf("condition was printed or evaluated before policy refusal: %s", output)
				}
			})
		}
	}
}
