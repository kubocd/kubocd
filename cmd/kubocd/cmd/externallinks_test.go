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
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kubocd/cmd/kubocd/cmd/cmn"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestChartSourceOptionsReachCommands(t *testing.T) {
	for _, item := range []struct {
		command *cobra.Command
		options *cmn.SourceOptions
	}{
		{packageCmd, &packageParams.sourceOptions},
		{dumpPackageCmd, &dumpPackageParams.sourceOptions},
		{renderCmd, &renderParams.sourceOptions},
	} {
		t.Run(item.command.Name(), func(t *testing.T) {
			flag := item.command.PersistentFlags().Lookup("allowExternalLinks")
			if flag == nil || flag.DefValue != "false" {
				t.Fatal("external source reads are not disabled by default")
			}
			saved, changed := *item.options, flag.Changed
			t.Cleanup(func() { *item.options = saved; flag.Changed = changed })
			if err := item.command.PersistentFlags().Set("allowExternalLinks", "true"); err != nil {
				t.Fatal(err)
			}
			if !item.options.AllowExternalLinks {
				t.Fatal("command did not pass the source opt-out to its options")
			}
		})
	}
}

const (
	sourceRenderCommand  = "render"
	sourcePackageCommand = "package"
	sourceDumpCommand    = "dump"
	sourceChartsFlag     = "--charts"
)

func TestChartSourceCommandProcess(t *testing.T) {
	encoded := os.Getenv("KUBOCD_SOURCE_TEST_ARGS")
	if encoded == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(encoded), &args); err != nil {
		t.Fatal(err)
	}
	if len(args) > 0 && args[0] == sourceRenderCommand {
		http.DefaultTransport = sourceConfigTransport{}
	}
	rootCmd.SetArgs(args)
	Execute()
}

type sourceConfigTransport struct{}

func (sourceConfigTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body string
	switch request.URL.Path {
	case "/apis":
		body = `{
  "kind": "APIGroupList",
  "apiVersion": "v1",
  "groups": [
    {
      "name": "kubocd.kubotal.io",
      "versions": [
        {
          "groupVersion": "kubocd.kubotal.io/v1alpha1",
          "version": "v1alpha1"
        }
      ],
      "preferredVersion": {
        "groupVersion": "kubocd.kubotal.io/v1alpha1",
        "version": "v1alpha1"
      }
    }
  ]
}`
	case "/api":
		body = `{"kind":"APIVersions","apiVersion":"v1","versions":[]}`
	case "/apis/kubocd.kubotal.io/v1alpha1":
		body = `{
  "kind": "APIResourceList",
  "apiVersion": "v1",
  "groupVersion": "kubocd.kubotal.io/v1alpha1",
  "resources": [
    {
      "name": "configs",
      "singularName": "config",
      "namespaced": true,
      "kind": "Config",
      "verbs": [
        "get",
        "list"
      ]
    }
  ]
}`
	case "/apis/kubocd.kubotal.io/v1alpha1/namespaces/kubocd/configs":
		body = `{"kind":"ConfigList","apiVersion":"kubocd.kubotal.io/v1alpha1","items":[]}`
	default:
		return nil, fmt.Errorf("unexpected Kubernetes request %s", request.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestChartSourceOptionsAcquire(t *testing.T) {
	for _, command := range []string{sourcePackageCommand, sourceDumpCommand, sourceRenderCommand} {
		t.Run(command, func(t *testing.T) {
			base := t.TempDir()
			write := func(name, data string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			chart := filepath.Join(base, "chart")
			write(filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 0.1.0\n")
			sentinel := "external-regular-file-sentinel"
			outside := filepath.Join(base, "sentinel.yaml")
			write(outside, sentinel)
			if err := os.MkdirAll(filepath.Join(chart, "templates"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(chart, "templates", "sentinel.yaml")); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(base, "bin", "helm"), "#!/bin/sh\nexit 0\n")
			manifest := filepath.Join(base, "package.yaml")
			write(manifest, fmt.Sprintf(`apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
- name: main
  source:
    local:
      path: %q
`, chart))
			release := filepath.Join(base, "release.yaml")
			write(release, `apiVersion: kubocd.kubotal.io/v1alpha1
kind: Release
metadata:
  name: demo
  namespace: default
spec:
  package:
    repository: example.invalid/demo
    tag: 0.1.0
`)
			config := filepath.Join(base, "kubeconfig")
			write(config, `apiVersion: v1
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
			for _, allow := range []bool{false, true} {
				work := filepath.Join(base, fmt.Sprintf("work-%t", allow))
				output := filepath.Join(base, fmt.Sprintf("out-%t", allow))
				args := make([]string, 0, 10)
				switch command {
				case sourcePackageCommand:
					args = []string{sourcePackageCommand, manifest, "--ociRepoPrefix=invalid registry!"}
				case sourceDumpCommand:
					args = []string{sourceDumpCommand, sourcePackageCommand, manifest, sourceChartsFlag, "--output", output}
				case sourceRenderCommand:
					args = []string{sourceRenderCommand, release, manifest, "--output", output}
				}
				args = append(args, "--workDir", work, fmt.Sprintf("--allowExternalLinks=%t", allow))
				encoded, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestChartSourceCommandProcess$")
				child.Env = append(os.Environ(), "KUBOCD_SOURCE_TEST_ARGS="+string(encoded),
					"PATH="+filepath.Join(base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
					"KUBECONFIG="+config, "KUBERNETES_SERVICE_HOST=")
				log, runErr := child.CombinedOutput()
				cancel()
				if !allow {
					if runErr == nil || !strings.Contains(string(log), "outside its trusted root") {
						t.Fatalf("command did not refuse external source: %v\n%s", runErr, log)
					}
					continue
				}
				archive := filepath.Join(work, "local2-workdir", "main.tgz")
				f, err := os.Open(archive)
				if err != nil {
					t.Fatalf("command did not forward source opt-out: %v\n%s", err, log)
				}
				gz, err := gzip.NewReader(f)
				if err != nil {
					_ = f.Close()
					t.Fatalf("command did not acquire external source: %v\n%s", err, log)
				}
				tr := tar.NewReader(gz)
				found := false
				for {
					h, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if h.Name == "main/templates/sentinel.yaml" {
						data, err := io.ReadAll(tr)
						if err != nil {
							t.Fatal(err)
						}
						found = string(data) == sentinel
					}
				}
				_ = gz.Close()
				_ = f.Close()
				if !found {
					t.Fatalf("command did not acquire external sentinel\n%s", log)
				}
				if command == sourceDumpCommand && runErr != nil {
					t.Fatalf("dump failed after acquisition: %v\n%s", runErr, log)
				}
			}
		})
	}
}
