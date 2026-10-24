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

//nolint:goconst // test inputs read better inline
package cmd

import (
	"bytes"
	"errors"
	"io"
	kapi "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/controller"
	"kubocd/internal/kubopackage"
	"kubocd/internal/tmpl"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// previewPackage has a module that installs in a namespace of its own, one in
// the target namespace of the Release, and a disabled one.
const previewPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      replicas:
        type: integer
        default: 1
modules:
  - name: main
    targetNamespace: other-ns
    values: |
      replicaCount: {{ .Parameters.replicas }}
      name: from-package
    source:
      local:
        path: ./chart
  - name: side
    values: |
      side: true
    source:
      local:
        path: ./chart
  - name: off
    enabled: false
    source:
      local:
        path: ./chart
`

// previewNamespace is the namespace of the Releases of the preview tests.
const previewNamespace = "apps"

// helmCall is what a fake helm command got: its arguments, and the values of
// the file that --values names.
type helmCall struct {
	args   []string
	values map[string]interface{}
}

// target returns the namespace and the release name that helm template got.
func (c helmCall) target(t *testing.T) (namespace string, name string) {
	t.Helper()
	i := slices.Index(c.args, "-n")
	if c.args[0] != "template" || i < 0 || i+2 >= len(c.args) {
		t.Fatalf("unexpected helm arguments %q", c.args)
	}
	return c.args[i+1], c.args[i+2]
}

// preview is a Release rendered with previewPackage, as kubocd render does
// before it runs helm.
type preview struct {
	release      *kapi.Release
	pckContainer *kubopackage.PckContainer
	rendered     *kubopackage.Rendered
	store        configstore.ConfigStore
	output       string
	chartsDir    string
}

// run previews the modules of the Release with the helm command given, and
// writes what helm writes on its standard error to diagnostics.
func (p preview) run(helm helmCommand, diagnostics io.Writer) error {
	return previewModules(p.output, p.chartsDir, p.release, p.pckContainer, p.rendered, "kcd-web", p.store, helm,
		diagnostics)
}

// renderForPreview renders release with previewPackage as kubocd render does,
// and puts a chart folder for each module in the output.
func renderForPreview(t *testing.T, release *kapi.Release) preview {
	t.Helper()
	pck := &kubopackage.Package{}
	if err := yaml.UnmarshalStrict([]byte(previewPackage), pck); err != nil {
		t.Fatal(err)
	}
	chart := kubopackage.ChartRef{Name: "demo", Version: "1.0.0"}
	status := &kubopackage.Status{
		ChartByModule: map[string]kubopackage.ChartRef{"main": chart, "side": chart, "off": chart},
	}
	store := configstore.New()
	pckContainer := &kubopackage.PckContainer{}
	err := pckContainer.SetPackage(pck, status, "0.1.0@sha256:0123456789abcdef", store, tmpl.PackageFunctions(false))
	if err != nil {
		t.Fatal(err)
	}
	controller.GroomRelease(release, logr.Discard(), store)
	parameters, err := controller.HandleParameters(release, map[string]interface{}{}, store, pckContainer)
	if err != nil {
		t.Fatal(err)
	}
	model, err := controller.BuildModel(map[string]interface{}{}, parameters, release, store)
	if err != nil {
		t.Fatal(err)
	}
	model["Inputs"] = map[string]interface{}{}
	model["InputLists"] = map[string]interface{}{}
	rendered, err := pckContainer.Package.Render(model, release)
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	chartsDir := filepath.Join(output, "charts")
	for module := range status.ChartByModule {
		if err := os.MkdirAll(filepath.Join(chartsDir, module, "demo"), 0o755); err != nil {
			t.Fatal(err)
		}
		chartFile := filepath.Join(chartsDir, module, "demo", "Chart.yaml")
		if err := os.WriteFile(chartFile, []byte("name: demo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return preview{release: release, pckContainer: pckContainer, rendered: rendered, store: store, output: output,
		chartsDir: chartsDir}
}

// previewOf renders release with previewPackage as kubocd render does, then
// returns, by module, what previewModules gave to helm.
func previewOf(t *testing.T, release *kapi.Release) map[string]helmCall {
	t.Helper()
	p := renderForPreview(t, release)
	calls := map[string]helmCall{}
	helm := func(args ...string) ([]byte, []byte, error) {
		call := helmCall{args: args}
		i := slices.Index(args, "--values")
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("helm got no values file: %q", args)
		}
		data, err := os.ReadFile(args[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, &call.values); err != nil {
			t.Fatal(err)
		}
		module := filepath.Base(filepath.Dir(args[i+1]))
		if _, ok := calls[module]; ok {
			t.Fatalf("helm is called twice for the module %s", module)
		}
		calls[module] = call
		return []byte("# manifests of " + module + "\n"), []byte(helmDebugLine), nil
	}
	diagnostics := &bytes.Buffer{}
	if err := p.run(helm, diagnostics); err != nil {
		t.Fatal(err)
	}
	if got := diagnostics.String(); got != strings.Repeat(helmDebugLine, len(calls)) {
		t.Fatalf("got the diagnostics %q, want the standard error of each call of helm", got)
	}
	for module := range calls {
		manifests, err := os.ReadFile(filepath.Join(p.output, "modules", module, "manifests.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		// The first line is the header that kubocd render writes in each file
		if _, body, _ := strings.Cut(string(manifests), "\n"); body != "# manifests of "+module+"\n\n" {
			t.Fatalf("manifests.yaml of the module %s holds %q, want the standard output of helm only", module, manifests)
		}
	}
	return calls
}

// helmDebugLine is a line that helm template --debug writes on its standard
// error.
const helmDebugLine = "install.go:225: 2026-10-24 10:00:00.000000 +0200 CEST [debug] Original chart version: \"\"\n"

// previewRelease is the Release web of the namespace apps, with the
// parameters given, and patching the spec of the HelmRelease of the module
// main with specPatch.
func previewRelease(name string, specPatch string) *kapi.Release {
	release := &kapi.Release{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: previewNamespace}}
	release.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(`{"replicas":2}`)}
	if specPatch != "" {
		release.Spec.ModuleOverrides = []kapi.ModuleOverrideSpec{{Module: "main",
			SpecPatch: &apiextensionsv1.JSON{Raw: []byte(specPatch)}}}
	}
	return release
}

func TestRenderPreviewsAModuleInTheNamespaceAndWithTheValuesOfItsHelmRelease(t *testing.T) {
	calls := previewOf(t, previewRelease("web", `{"values":{"replicaCount":3},"releaseName":"custom"}`))
	if _, ok := calls["off"]; ok || len(calls) != 2 {
		t.Fatalf("got helm calls for %v, want one for main and one for side", slices.Sorted(maps.Keys(calls)))
	}
	if namespace, _ := calls["main"].target(t); namespace != "other-ns" {
		t.Errorf("module main is previewed in %q, want other-ns, the target namespace of its HelmRelease", namespace)
	}
	if _, name := calls["main"].target(t); name != "custom" {
		t.Errorf("module main is previewed as %q, want custom, the releaseName of its HelmRelease", name)
	}
	want := map[string]interface{}{"replicaCount": float64(3), "name": "from-package"}
	if !reflect.DeepEqual(calls["main"].values, want) {
		t.Errorf("module main is previewed with the values %v, want %v, those of its HelmRelease", calls["main"].values, want)
	}
	if namespace, name := calls["side"].target(t); namespace != previewNamespace || name != "web-side" {
		t.Errorf("module side is previewed in %q as %q, want apps as web-side", namespace, name)
	}
	if want := map[string]interface{}{"side": true}; !reflect.DeepEqual(calls["side"].values, want) {
		t.Errorf("module side is previewed with the values %v, want %v", calls["side"].values, want)
	}
}

func TestRenderPreviewsAModuleWithoutTargetNamespaceNorReleaseNameAsFluxInstallsIt(t *testing.T) {
	calls := previewOf(t, previewRelease("web", `{"targetNamespace":null,"releaseName":null}`))
	if namespace, name := calls["main"].target(t); namespace != previewNamespace || name != "web-main" {
		t.Errorf("module main is previewed in %q as %q, want apps, the namespace of its HelmRelease, as web-main", namespace,
			name)
	}
}

func TestRenderPreviewsALongReleaseNameShortenedAsFluxDoes(t *testing.T) {
	calls := previewOf(t, previewRelease("a-release-with-a-long-name",
		`{"targetNamespace":"a-namespace-of-a-long-name","releaseName":null}`))
	namespace, name := calls["main"].target(t)
	// The SHA-256 of a-namespace-of-a-long-name-a-release-with-a-long-name-main starts with 910c28b9d047
	want := "a-namespace-of-a-long-name-a-release-wit-910c28b9d047"
	if namespace != "a-namespace-of-a-long-name" || name != want {
		t.Errorf("module main is previewed in %q as %q, want a-namespace-of-a-long-name as %s", namespace, name, want)
	}
}

func TestTheReleaseNameIsShortenedPast53BytesAsFluxDoes(t *testing.T) {
	cases := map[string]struct{ name, want string }{
		"53 bytes": {strings.Repeat("a", 53), strings.Repeat("a", 53)},
		// The SHA-256 of 54 a starts with a3f01b693925
		"54 bytes": {strings.Repeat("a", 54), strings.Repeat("a", 40) + "-a3f01b693925"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			helmRelease := &fluxv2.HelmRelease{ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: previewNamespace},
				Spec: fluxv2.HelmReleaseSpec{ReleaseName: c.name}}
			if got := fluxReleaseName(helmRelease); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestRunHelmReturnsItsStandardOutputApartFromItsStandardError(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf 'debug line\\n' >&2\nprintf -- '---\\nkind: ConfigMap\\n'\n" +
		"printf 'another debug line\\n' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "helm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdout, stderr, err := runHelm("template", "--debug")
	if err != nil || string(stdout) != "---\nkind: ConfigMap\n" || string(stderr) != "debug line\nanother debug line\n" {
		t.Fatalf("got the output %q, the error output %q and the error %v, want them apart", stdout, stderr, err)
	}
}

func TestAFailingHelmTemplateStopsThePreviewAndPrintsBothOutputs(t *testing.T) {
	failing := func(args ...string) ([]byte, []byte, error) {
		return []byte("---\nbroken: [\n"), []byte("Error: YAML parse error\n"), errors.New("exit status 1")
	}
	p := renderForPreview(t, previewRelease("web", ""))
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = write
	err = p.run(failing, io.Discard)
	os.Stdout = stdout
	_ = write.Close()
	printed, _ := io.ReadAll(read)
	if err == nil || err.Error() != "failed to generate manifest: exit status 1" {
		t.Fatalf("got the error %v, want the failure of helm", err)
	}
	text := string(printed)
	if !strings.Contains(text, "---\nbroken: [\n") || !strings.Contains(text, "Error: YAML parse error\n") {
		t.Fatalf("printed %q, want both outputs of helm", printed)
	}
}
