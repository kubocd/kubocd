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

//nolint:goconst // Keep fixture values beside their assertions.
package cmd

import (
	"io"
	kapi "kubocd/api/v1alpha1"
	"kubocd/cmd/kubocd/cmd/cmn"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRenderContainsModulePaths(t *testing.T) {
	for _, name := range []string{"../victim", "../../../victim/m"} {
		t.Run(name, func(t *testing.T) {
			p := renderForPreview(t, &kapi.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps"}})
			p.output = filepath.Join(p.output, "out", "nested")
			p.chartsDir = filepath.Join(p.output, "charts")
			module := p.pckContainer.Package.Modules[0]
			old := module.Name
			module.Name = name
			p.pckContainer.Package.Modules = p.pckContainer.Package.Modules[:1]
			p.rendered.ModuleRenderedByName[name] = p.rendered.ModuleRenderedByName[old]
			p.pckContainer.Status.ChartByModule[name] = p.pckContainer.Status.ChartByModule[old]
			called := false
			err := p.run(func(_ ...string) ([]byte, []byte, error) { called = true; return nil, nil, nil }, io.Discard)
			if called {
				t.Fatal("Helm received an escaping module path")
			}
			if _, statErr := os.Stat(filepath.Join(p.output, "modules", name)); statErr == nil {
				t.Fatal("escaped path was created")
			}
			if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("module path was not refused: %v", err)
			}
		})
	}
}

func TestRenderContainsReleasePaths(t *testing.T) {
	if base := os.Getenv("KUBOCD_RELEASE_PATH_PROBE"); base != "" {
		renderParams.output = filepath.Join(base, "output")
		renderParams.workDir = filepath.Join(base, "work")
		renderLog = logr.Discard()
		renderCmd.Run(renderCmd, []string{filepath.Join(base, "release.yaml")})
		return
	}
	base := t.TempDir()
	victim := filepath.Join(base, "victim")
	if err := os.MkdirAll(victim, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{".transient": "", "sentinel": "keep"} {
		if err := os.WriteFile(filepath.Join(victim, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	release := "apiVersion: kubocd.kubotal.io/v1alpha1\nkind: Release\nmetadata:\n  name: ../victim\n"
	if err := os.WriteFile(filepath.Join(base, "release.yaml"), []byte(release), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRenderContainsReleasePaths$")
	command.Env = append(os.Environ(), "KUBOCD_RELEASE_PATH_PROBE="+base, "KUBECONFIG="+filepath.Join(base, "missing"),
		"KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("escaping Release was accepted")
	}
	data, readErr := os.ReadFile(filepath.Join(victim, "sentinel"))
	if readErr != nil || string(data) != "keep" {
		t.Fatal("outside sentinel changed")
	}
	if !strings.Contains(string(output), "trusted parent") {
		t.Fatalf("wrong Release refusal: %s", output)
	}
}

func TestRenderStdoutContainsWorkingFiles(t *testing.T) {
	for _, kind := range []string{"healthy", "module", "charts link", "modules link"} {
		t.Run(kind, func(t *testing.T) {
			p := renderForPreview(t, &kapi.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps"}})
			t.Chdir(p.output)
			if err := os.WriteFile("sentinel", []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if strings.HasSuffix(kind, " link") {
				name := strings.TrimSuffix(kind, " link")
				if err := os.RemoveAll(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, name); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "module" {
				module := p.pckContainer.Package.Modules[0]
				old := module.Name
				module.Name = "../victim"
				p.rendered.ModuleRenderedByName[module.Name] = p.rendered.ModuleRenderedByName[old]
				p.pckContainer.Status.ChartByModule[module.Name] = p.pckContainer.Status.ChartByModule[old]
			}
			capture, err := os.CreateTemp(t.TempDir(), "stdout")
			if err != nil {
				t.Fatal(err)
			}
			previous := os.Stdout
			os.Stdout = capture
			defer func() { os.Stdout = previous; _ = capture.Close() }()
			cmn.Dump("", "probe.yaml", map[string]string{"key": "value"})
			err = previewModules("", "charts", p.release, p.pckContainer, p.rendered, "kcd-web", p.store,
				func(_ ...string) ([]byte, []byte, error) { return []byte("rendered\n"), nil, nil }, io.Discard)
			os.Stdout = previous
			if kind == "healthy" {
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile("modules/main/manifests.yaml")
				if readErr != nil || string(data) != "# ====================================  manifests.yaml:\nrendered\n\n" {
					t.Fatalf("stdout mode lost working files: %s %v", data, readErr)
				}
				data, readErr = os.ReadFile(capture.Name())
				if readErr != nil || !strings.Contains(string(data), "key: value") {
					t.Fatalf("top-level dump left stdout: %s %v", data, readErr)
				}
			} else if err == nil || !strings.Contains(err.Error(), "trusted parent") {
				t.Fatalf("stdout path was not refused: %v", err)
			}
			if _, err := os.Stat(".transient"); !os.IsNotExist(err) {
				t.Fatal("stdout marked the current directory")
			}
			data, err := os.ReadFile("sentinel")
			if err != nil || string(data) != "keep" {
				t.Fatal("stdout cleaned the current directory")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("stdout wrote outside the current directory")
			}
			if _, err := os.Stat("probe.yaml"); !os.IsNotExist(err) {
				t.Fatalf("top-level dump created a file: %v", err)
			}
		})
	}
}

func TestRenderRefusesLinkedChartBeforeHelm(t *testing.T) {
	p := renderForPreview(t, &kapi.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: previewNamespace}})
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside-read-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	templates := filepath.Join(p.chartsDir, "main", "demo", "templates")
	if err := os.MkdirAll(templates, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(templates, "leak.yaml")); err != nil {
		t.Fatal(err)
	}
	called := false
	err := p.run(func(args ...string) ([]byte, []byte, error) {
		data, readErr := os.ReadFile(filepath.Join(args[5], "templates", "leak.yaml"))
		if readErr == nil && string(data) == "outside-read-canary" {
			called = true
		}
		return nil, nil, nil
	}, io.Discard)
	if called {
		t.Fatal("Helm read outside the chart tree")
	}
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("chart link was not refused before Helm: %v", err)
	}
}

func TestRenderModuleCannotCleanResolvedParent(t *testing.T) {
	p := renderForPreview(t, &kapi.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps"}})
	t.Chdir(p.output)
	module := p.pckContainer.Package.Modules[0]
	old := module.Name
	module.Name = "a/link/.."
	p.pckContainer.Package.Modules = p.pckContainer.Package.Modules[:1]
	p.rendered.ModuleRenderedByName[module.Name] = p.rendered.ModuleRenderedByName[old]
	p.pckContainer.Status.ChartByModule[module.Name] = p.pckContainer.Status.ChartByModule[old]
	for _, dir := range []string{"modules/a", "modules/b"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{".transient": "", "sentinel": "keep"} {
		if err := os.WriteFile(filepath.Join("modules", name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../b", "modules/a/link"); err != nil {
		t.Fatal(err)
	}
	called := false
	err := previewModules("", "charts", p.release, p.pckContainer, p.rendered, "kcd-web", p.store,
		func(_ ...string) ([]byte, []byte, error) { called = true; return nil, nil, nil }, io.Discard)
	got, readErr := os.ReadFile("modules/sentinel")
	if readErr != nil || string(got) != "keep" {
		t.Fatalf("render removed its modules parent sentinel: %q, %v", got, readErr)
	}
	if err == nil || called {
		t.Fatalf("render accepted a module resolving to its parent: %v", err)
	}
}
