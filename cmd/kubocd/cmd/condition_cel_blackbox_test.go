//nolint:goconst // Command fixtures and expected diagnostics stay together.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestConditionCELCommandProcess(t *testing.T) {
	encoded := os.Getenv("KUBOCD_CEL_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		t.Fatal(err)
	}
	http.DefaultTransport = celCommandTransport{}
	rootCmd.SetArgs(args)
	Execute()
}

type celCommandTransport struct{}

func (celCommandTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body string
	switch request.URL.Path {
	case "/api":
		body = `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`
	case "/api/v1":
		body = `{"kind":"APIResourceList","groupVersion":"v1",
 "resources":[{"name":"secrets","kind":"Secret","namespaced":true,"verbs":["get"]}]}`
	case "/api/v1/namespaces/target/secrets/db":
		body = `{"apiVersion":"v1","kind":"Secret","metadata":{"namespace":"target","name":"db"},
 "data":{"password":"b2JqZWN0LWNhbmFyeQ=="}}`
	default:
		return (sourceConfigTransport{}).RoundTrip(request)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func runCELCommand(t *testing.T, args []string, env ...string) (string, error) {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConditionCELCommandProcess$")
	child.Env = append(os.Environ(), "KUBOCD_CEL_TEST_ARGS="+string(encoded), "KUBERNETES_SERVICE_HOST=")
	child.Env = append(child.Env, env...)
	output, err := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("command did not finish: %v\n%s", ctx.Err(), output)
	}
	return string(output), err
}

func TestConditionCELFlags(t *testing.T) {
	for _, cmd := range []*cobra.Command{controllerCmd, renderCmd} {
		for _, tc := range []struct{ name, def, typ, negative string }{
			{"conditionCelCostLimit", "1000000", "int64", "-1"},
			{"conditionCelTimeout", "1s", "duration", "-1s"},
		} {
			t.Run(cmd.Name()+"/"+tc.name, func(t *testing.T) {
				flag := cmd.PersistentFlags().Lookup(tc.name)
				if flag == nil {
					t.Errorf("missing flag --%s", tc.name)
				} else {
					if flag.DefValue != tc.def || flag.Value.Type() != tc.typ {
						t.Errorf("flag default/type = %s/%s", flag.DefValue, flag.Value.Type())
					}
					if !strings.Contains(flag.Usage, "0") || !strings.Contains(flag.Usage, "evaluation") {
						t.Errorf("incomplete help: %s", flag.Usage)
					}
				}
				args := []string{cmd.Name(), "--" + tc.name + "=" + tc.negative}
				if cmd == renderCmd {
					args = append(args, "/missing-release-canary")
				}
				output, err := runCELCommand(t, args, "KUBECONFIG=/missing-kubeconfig-canary")
				exit, isExit := err.(*exec.ExitError)
				want := tc.name + " must not be negative"
				if !isExit || exit.ExitCode() != 2 || !strings.Contains(output, want) {
					t.Fatalf("want exit 2 and %q, got %v\n%s", want, err, output)
				}
			})
		}
	}
}

func TestRenderConditionCELLimits(t *testing.T) {
	base := t.TempDir()
	write := func(name, data string) string {
		t.Helper()
		p := filepath.Join(base, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
		return p
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
	expr := "true"
	for i := 6; i > 0; i-- {
		expr = fmt.Sprintf("[0,1,2,3,4,5,6,7,8,9].all(x%d, x%d >= 0 && %s)", i, i, expr)
	}
	for _, tc := range []struct {
		name, expr, want string
		flags            []string
		defaultBudget    bool
	}{
		{name: "ordinary", expr: "'password' in data"},
		{name: "default budget", expr: expr, defaultBudget: true},
		{name: "cost budget", expr: expr, want: "condition CEL cost limit exceeded",
			flags: []string{"--conditionCelCostLimit=1000000", "--conditionCelTimeout=0"}},
		{name: "cost disabled", expr: expr, flags: []string{"--conditionCelCostLimit=0", "--conditionCelTimeout=10s"}},
		{name: "both disabled", expr: expr, flags: []string{"--conditionCelCostLimit=0", "--conditionCelTimeout=0"}},
		{name: "deadline", expr: expr, want: "condition CEL evaluation deadline exceeded",
			flags: []string{"--conditionCelCostLimit=0", "--conditionCelTimeout=1ns"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := write("package.yaml", fmt.Sprintf(`apiVersion: v1alpha1
name: demo
tag: 0.1.0
conditions: |
  - kind: Secret
    name: db
    cel: %q
modules:
- name: main
  source:
    local:
      path: %q
`, tc.expr, filepath.Join(base, "chart")))
			args := []string{"render", release, manifest, "--output=" + filepath.Join(t.TempDir(), "out"),
				"--workDir", t.TempDir()}
			args = append(args, tc.flags...)
			output, err := runCELCommand(t, args, "KUBECONFIG="+config,
				"PATH="+filepath.Join(base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			if tc.defaultBudget {
				lines := strings.Split(output, "\n")
				cost := slices.Contains(lines, "ERROR: condition#0: error: condition CEL cost limit exceeded")
				deadline := slices.Contains(lines, "ERROR: condition#0: error: condition CEL evaluation deadline exceeded")
				if err == nil || cost == deadline {
					t.Fatalf("want definitive budget exhaustion, got %v\n%s", err, output)
				}
			} else if tc.want == "" {
				if err != nil {
					t.Fatalf("render: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(output, "condition#0: error: "+tc.want) {
				t.Fatalf("want definitive %q, got %v\n%s", tc.want, err, output)
			}
		})
	}
}
