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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const (
	ociTestPushPrefix = "--ociRepoPrefix=registry.test"
	ociTestHTTPFlag   = "--insecure"
	ociTestAllowFlag  = "--allowInsecureCredentials"
	ociTestFalse      = "false"
	ociTestReference  = "registry.test/demo:0.1.0"
)

func TestOCICommandProcess(t *testing.T) {
	encoded := os.Getenv("KUBOCD_OCI_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		t.Fatal(err)
	}
	f := newOCIRegistryFixture(t)
	f.basic = true
	f.recordFile = os.Getenv("KUBOCD_OCI_TEST_RECORDS")
	if os.Getenv("KUBOCD_OCI_TEST_TOKEN") == ociTestTrue {
		f.basic = false
		f.realm = "http://registry.test/token"
	}
	if os.Getenv("KUBOCD_OCI_TEST_ANONYMOUS") == ociTestTrue {
		f.basic = false
	}
	if kind := os.Getenv("KUBOCD_OCI_TEST_CHAIN"); kind != "" {
		configureOCIChain(f, kind, "registry.test:8443", "http")
	}
	if os.Getenv("KUBOCD_OCI_TEST_USERINFO") == ociTestTrue {
		f.redirect = func(r *http.Request) string {
			if r.URL.Host == ociTestHost {
				return "http://u:server-secret@registry.test:8443" + r.URL.Path
			}
			return ""
		}
	}
	http.DefaultTransport = f
	rootCmd.SetArgs(args)
	Execute()
	if len(f.pushed) > 0 {
		if err := os.WriteFile(f.recordFile+".manifest", f.pushed, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

type ociCommandFixture struct{ base, manifest, local, release, config string }

func newOCICommandFixture(t *testing.T) ociCommandFixture {
	t.Helper()
	b := t.TempDir()
	write := func(name, data string) string {
		t.Helper()
		p := filepath.Join(b, name)
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
	manifest := write("package.yaml", `apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    oci:
      repository: registry.test/demo
      tag: 0.1.0
      insecure: true
`)
	local := write("local.yaml", fmt.Sprintf(`apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    local:
      path: %q
`, filepath.Join(b, "chart")))
	release := write("release.yaml", `apiVersion: kubocd.kubotal.io/v1alpha1
kind: Release
metadata:
  name: demo
  namespace: default
spec:
  package:
    repository: registry.test/demo
    tag: 0.1.0
    insecure: true
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
	return ociCommandFixture{b, manifest, local, release, config}
}
func (f ociCommandFixture) run(t *testing.T, args []string, env ...string) (string, []ociRequestRecord, error) {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "requests")
	args = append(append([]string(nil), args...), "--workDir", filepath.Join(dir, "work"))
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOCICommandProcess$")
	child.Env = append(os.Environ(), "KUBOCD_OCI_TEST_ARGS="+string(encoded), "KUBOCD_OCI_TEST_RECORDS="+record,
		"PATH="+filepath.Join(f.base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"KUBECONFIG="+f.config, "KUBERNETES_SERVICE_HOST=")
	child.Env = append(child.Env, env...)
	out, runErr := child.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI timeout: %s", out)
	}
	var records []ociRequestRecord
	data, err := os.ReadFile(record)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r ociRequestRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return string(out), records, runErr
}
func TestOCICommandCredentialOptions(t *testing.T) {
	f := newOCIRegistryFixture(t)
	installOCIFixture(t, f, "deprecated")
	fixture := newOCICommandFixture(t)
	cases := []struct {
		name string
		args []string
	}{
		{ociTestPackagePush, []string{sourcePackageCommand, fixture.local, ociTestPushPrefix, "--plainHTTP"}},
		{"packageModule", []string{sourcePackageCommand, fixture.manifest, ociTestPushPrefix}},
		{"dumpOCI", []string{sourceDumpCommand, ociTestCommand, "oci://registry.test/demo:0.1.0", ociTestHTTPFlag}},
		{"dumpPackage", []string{sourceDumpCommand, sourcePackageCommand, "oci://registry.test/demo:0.1.0", ociTestHTTPFlag}},
		{"dumpModule", []string{sourceDumpCommand, sourcePackageCommand, fixture.manifest, sourceChartsFlag}},
		{"renderRemote", []string{sourceRenderCommand, fixture.release, "--allowReleaseInsecureSource"}},
		{"renderModule", []string{sourceRenderCommand, fixture.release, fixture.manifest, "--allowReleaseInsecureSource"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, allow := range []bool{false, true} {
				t.Run(fmt.Sprint(allow), func(t *testing.T) {
					args := append([]string(nil), tc.args...)
					// The default witness uses no new flag, so it exercises the parent's network behavior.
					if allow {
						args = append(args, ociTestAllowFlag)
					}
					if tc.name != ociTestPackagePush && tc.name != "packageModule" && tc.name != "dumpOCI" {
						args = append(args, "--output", t.TempDir())
					}
					out, records, err := fixture.run(t, args)
					if !allow {
						if err == nil || !strings.Contains(out, ociTLSMessage) {
							t.Errorf("command did not refuse HTTP credentials: %v\n%s", err, out)
						}
						if len(records) > 0 {
							t.Errorf("refused command sent %d registry requests", len(records))
						}
						return
					}
					if err != nil {
						t.Fatalf("fallback command failed: %v\n%s", err, out)
					}
					auth, read, write := 0, 0, 0
					for _, r := range records {
						if strings.HasPrefix(r.URL, "http://") && strings.HasPrefix(r.Auth, "Basic ") {
							auth++
						}
						if strings.Contains(r.URL, "/blobs/") && r.Method == http.MethodGet {
							read++
						}
						if strings.Contains(r.URL, "/manifests/") && r.Method == http.MethodPut {
							write++
						}
					}
					if auth == 0 {
						t.Error("fallback did not authenticate over HTTP")
					}
					if tc.name != ociTestPackagePush && read == 0 {
						t.Error("fallback did not fetch content")
					}
					if strings.HasPrefix(tc.name, sourcePackageCommand) && write == 0 {
						t.Error("fallback did not push final manifest")
					}
				})
			}
		})
	}
	t.Run("explicitFalse", func(t *testing.T) {
		out, records, err := fixture.run(t, []string{sourceDumpCommand, ociTestCommand,
			ociTestReference, ociTestHTTPFlag, "--allowInsecureCredentials=false"})
		if err == nil || !strings.Contains(out, ociTLSMessage) || len(records) != 0 {
			t.Errorf("false opt-out failed: %v %s (%d requests)", err, out, len(records))
		}
	})
	t.Run("HTTPTokenFallback", func(t *testing.T) { testOCIHTTPTokenFallback(t, fixture) })
	t.Run(ociTestExplicitAnonymous, func(t *testing.T) {
		out, records, err := fixture.run(t, []string{sourceDumpCommand, ociTestCommand,
			ociTestReference, ociTestHTTPFlag, "-a"}, "KUBOCD_OCI_TEST_ANONYMOUS=true")
		if err != nil {
			t.Fatalf("anonymous command: %v %s", err, out)
		}
		for _, r := range records {
			if r.Auth != "" {
				t.Error("-a sent configured credentials")
			}
		}
	})
}
func TestCredentialOptionsRemainIndependent(t *testing.T) {
	commands := []*cobra.Command{packageCmd, dumpOciCmd, dumpPackageCmd, renderCmd}
	for _, command := range commands {
		flag := command.PersistentFlags().Lookup("allowInsecureCredentials")
		if flag == nil {
			t.Errorf("%s missing credential flag", command.CommandPath())
			continue
		}
		if flag.DefValue != ociTestFalse || flag.Value.String() != ociTestFalse {
			t.Errorf("%s credential flag not false by default", command.CommandPath())
		}
		saved, changed := flag.Value.String(), flag.Changed
		if err := command.PersistentFlags().Set("allowInsecureCredentials", ociTestTrue); err != nil {
			t.Fatal(err)
		}
		for _, other := range commands {
			if other != command {
				otherFlag := other.PersistentFlags().Lookup("allowInsecureCredentials")
				if otherFlag != nil && otherFlag.Value.String() != ociTestFalse {
					t.Errorf("%s changed %s", command.Name(), other.Name())
				}
			}
		}
		if err := flag.Value.Set(saved); err != nil {
			t.Fatal(err)
		}
		flag.Changed = changed
	}
	f := newOCIRegistryFixture(t)
	installOCIFixture(t, f, "deprecated")
	fixture := newOCICommandFixture(t)
	t.Run("GitUnaffected", func(t *testing.T) {
		manifest := filepath.Join(fixture.base, "git.yaml")
		data := `apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    git:
      url: http://u:fake-git-password@git.test/charts.git
      branch: main
      path: .
`
		if err := os.WriteFile(manifest, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		out, records, err := fixture.run(t, []string{sourcePackageCommand, manifest, ociTestPushPrefix, ociTestAllowFlag})
		if err == nil || !strings.Contains(out, "Git credentials require HTTPS") || len(records) != 0 {
			t.Errorf("OCI opt-out changed Git policy: %v %s", err, out)
		}
		if strings.Contains(out, "fake-git-password") {
			t.Error("Git password exposed with OCI opt-out")
		}
	})
	t.Run("GitVariableUnaffected", func(t *testing.T) {
		out, _, err := fixture.run(t, []string{sourceDumpCommand, ociTestCommand, ociTestReference,
			ociTestHTTPFlag, ociTestAllowFlag}, "KCD_GIT_TOKEN_HOSTS=invalid://host")
		if err != nil {
			t.Errorf("Git variable changed OCI behavior: %v %s", err, out)
		}
	})
}

func testOCIHTTPTokenFallback(t *testing.T, fixture ociCommandFixture) {
	t.Helper()
	out, records, err := fixture.run(t, []string{sourceDumpCommand, ociTestCommand,
		ociTestReference, ociTestHTTPFlag, ociTestAllowFlag}, "KUBOCD_OCI_TEST_TOKEN=true")
	if err != nil {
		t.Fatalf("token fallback: %v %s", err, out)
	}
	basic, bearer := false, false
	for _, r := range records {
		basic = basic || strings.Contains(r.URL, "/token") && strings.HasPrefix(r.Auth, "Basic ")
		bearer = bearer || r.Auth == "Bearer "+ociTestToken
	}
	if !basic || !bearer {
		t.Error("HTTP fallback did not exchange credentials for token")
	}
}
