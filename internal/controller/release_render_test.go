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
package controller

import (
	"context"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"kubocd/internal/tmpl"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

// testInputHelper finds connections by listing them, without field indexes.
type testInputHelper struct {
	client.Client
}

func (h *testInputHelper) FindConnectionsFromContract(ctx context.Context, namespace string, contract string) ([]kv1alpha1.Connection, ReconcileError) {
	list := &kv1alpha1.ConnectionList{}
	if err := h.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, NewReconcileError(err, false, "")
	}
	result := make([]kv1alpha1.Connection, 0, len(list.Items))
	for _, connection := range list.Items {
		if connection.Spec.Contract == contract {
			result = append(result, connection)
		}
	}
	return result, nil
}

func (h *testInputHelper) FindClusterConnectionsFromContract(context.Context, string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func (h *testInputHelper) FindOutputConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError) {
	return nil, nil
}

func (h *testInputHelper) FindOutputClusterConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func newTestHelper(t *testing.T, objects ...client.Object) *testInputHelper {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &testInputHelper{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()}
}

func newTestStore(t *testing.T) configstore.ConfigStore {
	t.Helper()
	store := configstore.New()
	err := store.AddConfigs(&kv1alpha1.ConfigList{Items: []kv1alpha1.Config{{
		ObjectMeta: metav1.ObjectMeta{Name: "config"},
		Spec: kv1alpha1.ConfigSpec{ImageRedirects: []*kv1alpha1.ImageRedirectSpec{{
			OldPrefix: "docker.io/", NewPrefix: "mirror.local/", ImagePullSecrets: []string{"pull-b", "pull-a"},
		}}},
	}}}, "kubocd")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newTestPackage(t *testing.T, manifest string) *kubopackage.PckContainer {
	t.Helper()
	pck := &kubopackage.Package{}
	if err := yaml.UnmarshalStrict([]byte(manifest), pck); err != nil {
		t.Fatalf("invalid test manifest: %v", err)
	}
	pckContainer := &kubopackage.PckContainer{}
	if err := pckContainer.SetPackage(pck, nil, "0.1.0@sha256:0123456789abcdef", nil, tmpl.PackageFunctions(false)); err != nil {
		t.Fatalf("invalid test package: %v", err)
	}
	return pckContainer
}

func newTestRelease(name string, parameters string, contexts ...kv1alpha1.NamespacedName) *kv1alpha1.Release {
	release := &kv1alpha1.Release{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
		Spec: kv1alpha1.ReleaseSpec{
			Package:  kv1alpha1.PackageSourceSpec{Repository: "quay.io/kubocd/packages/demo", Tag: "0.1.0"},
			Contexts: contexts,
		},
	}
	if parameters != "" {
		release.Spec.Parameters = &apiextensionsv1.JSON{Raw: []byte(parameters)}
	}
	return release
}

func readyConnection(name string, contract string, values string) *kv1alpha1.Connection {
	return &kv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps"},
		Spec:       kv1alpha1.ConnectionSpec{Contract: contract, Values: &apiextensionsv1.JSON{Raw: []byte(values)}},
		Status:     kv1alpha1.ConnectionStatus{Phase: kv1alpha1.ConnectionPhaseReady},
	}
}

// renderRelease assembles the template data of a Release and renders its
// package, in the order of ReleaseReconciler.reconcile2.
func renderRelease(helper BuildInputModelHelper, store configstore.ConfigStore, release *kv1alpha1.Release, pckContainer *kubopackage.PckContainer) (*kubopackage.Rendered, map[string]interface{}, error) {
	ctx := context.Background()
	GroomRelease(release, logr.Discard(), store)
	theContext, _, rerr := ComputeContext(ctx, helper, release, store, pckContainer.DefaultContext)
	if rerr != nil {
		return nil, nil, rerr
	}
	if err := pckContainer.ValidateContext(theContext); err != nil {
		return nil, nil, err
	}
	if len(pckContainer.ContextConnectionDecls) > 0 {
		theContext = DeepCopyTree(theContext)
	}
	parameters, err := HandleParameters(release, theContext, store, pckContainer)
	if err != nil {
		return nil, nil, err
	}
	model, err := BuildModel(theContext, parameters, release, store)
	if err != nil {
		return nil, nil, err
	}
	inputs, err := pckContainer.Package.RenderInputs(model, release.Namespace)
	if err != nil {
		return nil, nil, err
	}
	generatedInputs, refBindings, err := GenerateRefInputs(pckContainer.ParamConnectionDecls, pckContainer.ContextConnectionDecls, parameters, theContext, release.Namespace, inputs)
	if err != nil {
		return nil, nil, err
	}
	inputs = append(inputs, generatedInputs...)
	result, rerr := BuildInputModel(ctx, helper, inputs)
	if rerr != nil {
		return nil, nil, rerr
	}
	if len(result.Messages) > 0 {
		return nil, nil, fmt.Errorf("inputs not ready: %s", strings.Join(result.Messages, ", "))
	}
	if err := ApplyRefBindings(refBindings, result.InputModel, parameters, theContext); err != nil {
		return nil, nil, err
	}
	model["Inputs"] = result.InputModel
	model["InputLists"] = result.InputListModel
	rendered, err := pckContainer.Package.Render(model, release)
	return rendered, model, err
}

const assembledPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      replicas:
        type: integer
        default: 1
      db:
        type: connectionRef
        contract: database
        required: true
  context:
    properties:
      ingress:
        properties:
          className:
            type: string
            default: nginx
inputs:
  - contract: cache
    alias: cache
    allowMultiple: true
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      replicas: {{ .Parameters.replicas }}
      db: {{ .Parameters.db.host }}:{{ .Parameters.db.port }}
      ingressClass: {{ .Context.ingress.className }}
      domain: {{ .Context.domain }}
      cache: {{ .Inputs.cache.url }}
      caches: {{ len .InputLists.cache }}
      {{- $image := imageRedirect "docker.io/library/redis:7" . }}
      image: {{ $image.url }}
      pullSecrets: {{ toJson $image.imagePullSecrets }}
`

func assembledFixture(t *testing.T) (*testInputHelper, configstore.ConfigStore, *kubopackage.PckContainer, *kv1alpha1.Release) {
	t.Helper()
	clusterContext := &kv1alpha1.Context{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "contexts"},
		Spec:       kv1alpha1.ContextSpec{Context: &apiextensionsv1.JSON{Raw: []byte(`{"domain":"example.com"}`)}},
		Status:     kv1alpha1.ContextStatus{Phase: kv1alpha1.ContextPhaseReady},
	}
	helper := newTestHelper(t, clusterContext,
		readyConnection("pg", "database", `{"host":"pg.apps.svc","port":5432}`),
		readyConnection("cache-1", "cache", `{"url":"redis://cache-1"}`),
		readyConnection("cache-2", "cache", `{"url":"redis://cache-2"}`),
	)
	release := newTestRelease("demo", `{"replicas":2,"db":"pg"}`, kv1alpha1.NamespacedName{Namespace: "contexts", Name: "cluster"})
	return helper, newTestStore(t), newTestPackage(t, assembledPackage), release
}

func TestAssembledModelRenders(t *testing.T) {
	helper, store, pckContainer, release := assembledFixture(t)
	rendered, _, err := renderRelease(helper, store, release, pckContainer)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	got := rendered.ModuleRenderedByName["main"].Values
	want := map[string]interface{}{
		"replicas": float64(2), "db": "pg.apps.svc:5432", "ingressClass": "nginx", "domain": "example.com",
		"cache": "redis://cache-2", "caches": float64(2), "image": "mirror.local/library/redis:7",
		"pullSecrets": []interface{}{"pull-b", "pull-a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got values %v\nwant %v", got, want)
	}
}

func TestObjectInTheTemplateDataIsRejected(t *testing.T) {
	helper, store, pckContainer, release := assembledFixture(t)
	_, model, err := renderRelease(helper, store, release, pckContainer)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	model["Inputs"].(map[string]interface{})["cache"] = readyConnection("cache-1", "cache", `{}`)
	_, err = pckContainer.Package.Render(model, release)
	if want := "Inputs.cache is a *v1alpha1.Connection"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got error %v, want one containing %q", err, want)
	}
}

const sharedDefaultsPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      class:
        type: string
      labels:
        properties:
          team:
            type: string
            default: data
  context:
    properties:
      ingress:
        properties:
          className:
            type: string
            default: nginx
usage:
  a: '{{ $_ := set .Parameters "seen" "a" }}{{ $_ := set .Context.ingress "className" "usage-a" }}a'
  b: '{{ .Parameters.seen | default "none" }} {{ .Context.ingress.className }}'
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      {{- $_ := set .Parameters.labels "team" "values" }}
      ingressClass: {{ .Context.ingress.className }}
      paramClass: {{ .Parameters.class }}
      team: {{ .Parameters.labels.team }}
`

// The first Release reads the default Context, then modifies it, from its
// parameters. Without a Context object, ComputeContext hands out the cached
// default of the package itself.
func TestRenderingLeavesThePackageDefaultsIntact(t *testing.T) {
	helper, store := newTestHelper(t), newTestStore(t)
	pckContainer := newTestPackage(t, sharedDefaultsPackage)
	defaultContext, defaultParameters := DeepCopyTree(pckContainer.DefaultContext), DeepCopyTree(pckContainer.DefaultParameters)
	releases := []*kv1alpha1.Release{
		newTestRelease("first", "{\"class\":\"{{ .Context.ingress.className }}{{ $_ := set .Context.ingress `className` `pwned` }}\"}"),
		newTestRelease("second", `{"class":"{{ .Context.ingress.className }}"}`),
	}
	for _, release := range releases {
		rendered, _, err := renderRelease(helper, store, release, pckContainer)
		if err != nil {
			t.Fatalf("%s: render failed: %v", release.Name, err)
		}
		got := rendered.ModuleRenderedByName["main"].Values
		want := map[string]interface{}{"ingressClass": "nginx", "paramClass": "nginx", "team": "values"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got values %v, want %v", release.Name, got, want)
		}
		if got, want := rendered.Usage, map[string]string{"a": "a", "b": "none nginx"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got usage %v, want %v", release.Name, got, want)
		}
	}
	if !reflect.DeepEqual(pckContainer.DefaultContext, defaultContext) {
		t.Errorf("the cached default context changed: %v", pckContainer.DefaultContext)
	}
	if !reflect.DeepEqual(pckContainer.DefaultParameters, defaultParameters) {
		t.Errorf("the cached default parameters changed: %v", pckContainer.DefaultParameters)
	}
}

func TestDollarSequencesInAPackageStayLiteral(t *testing.T) {
	t.Setenv("SEC1_PROBE", "from-the-environment")
	manifest := filepath.Join(t.TempDir(), "original.yaml")
	err := os.WriteFile(manifest, []byte(`
apiVersion: v1alpha1
name: demo
tag: 0.1.0
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
    values: |
      home: ${HOME}
      probe: ${SEC1_PROBE}
      bucket: ${BUCKET#s3://}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	pck := &kubopackage.Package{}
	if err := misc.LoadYaml(manifest, pck); err != nil {
		t.Fatalf("load failed: %v", err)
	}
	pckContainer := &kubopackage.PckContainer{}
	if err := pckContainer.SetPackage(pck, nil, "0.1.0@sha256:0123456789abcdef", nil, tmpl.PackageFunctions(false)); err != nil {
		t.Fatalf("invalid test package: %v", err)
	}
	rendered, _, err := renderRelease(newTestHelper(t), newTestStore(t), newTestRelease("demo", ""), pckContainer)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	want := map[string]interface{}{"home": "${HOME}", "probe": "${SEC1_PROBE}", "bucket": "${BUCKET#s3://}"}
	if got := rendered.ModuleRenderedByName["main"].Values; !reflect.DeepEqual(got, want) {
		t.Fatalf("got values %v, want %v", got, want)
	}
}

const canaryPassword = "s3cr3t-SEC1-canary"

// leakPackage declares a connectionRef parameter, and inserts module fields
// and package fields written to fail with the password of the connection.
const leakPackage = `
apiVersion: v1alpha1
name: demo
tag: 0.1.0
schema:
  parameters:
    properties:
      db:
        type: connectionRef
        contract: database
        required: true
%s
modules:
  - name: main
    source:
      helmRepository:
        url: https://example.com/charts
        chart: demo
        version: 1.0.0
%s
`

func TestRenderErrorsDoNotQuoteConnectionValues(t *testing.T) {
	cases := map[string]struct {
		packageFields string
		moduleFields  string
		want          string
	}{
		"values": {"", `    values: |
      password: {{ .Parameters.db.password }}
      broken: [unclosed`, "could not render 'values' template: the rendered text is not valid YAML (line 2)"},
		"enabled":   {"", `    enabled: "{{ .Parameters.db.password }}"`, "could not render 'enabled' template: the rendered text is not a boolean"},
		"timeout":   {"", `    timeout: "{{ .Parameters.db.password }}"`, "could not render 'timeout' template: the rendered text is not a duration"},
		"dependsOn": {"", `    dependsOn: "[ {{ .Parameters.db.password }} ]"`, "dependsOn[0] does not name a module of the package"},
		"outputs, unknown field": {`outputs: |
  - contract: database
    {{ .Parameters.db.password }}: x`, "", "could not render 'outputs' template: the rendered text does not have the expected structure"},
		"outputs, duplicate name": {`outputs: |
  - contract: database
    name: {{ .Parameters.db.password }}
  - contract: database
    name: {{ .Parameters.db.password }}`, "", "output[1]: duplicate output name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			helper := newTestHelper(t, readyConnection("pg", "database", `{"host":"pg","password":"`+canaryPassword+`"}`))
			pckContainer := newTestPackage(t, fmt.Sprintf(leakPackage, c.packageFields, c.moduleFields))
			_, _, err := renderRelease(helper, newTestStore(t), newTestRelease("demo", `{"db":"pg"}`), pckContainer)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got error %v, want one containing %q", err, c.want)
			}
			if strings.Contains(err.Error(), canaryPassword[:7]) {
				t.Fatalf("the error quotes the connection password: %v", err)
			}
		})
	}
}

func TestParametersErrorDoesNotQuoteTheContext(t *testing.T) {
	release := newTestRelease("demo", `"value: {{ .Context.password }}\nbroken: [unclosed"`)
	_, err := HandleParameters(release, map[string]interface{}{"password": canaryPassword}, newTestStore(t), &kubopackage.PckContainer{})
	if err == nil || !strings.Contains(err.Error(), "could not render parameters template: the rendered text is not valid YAML") {
		t.Fatalf("got error %v, want a YAML error on the parameters", err)
	}
	if strings.Contains(err.Error(), canaryPassword[:7]) {
		t.Fatalf("the error quotes the context: %v", err)
	}
}
