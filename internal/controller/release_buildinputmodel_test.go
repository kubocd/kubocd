/*
Copyright 2026 Alexandre MABROUK

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

package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/misc"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type inputTestHelper struct {
	client.Client
}

func (h inputTestHelper) FindOutputConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError) {
	return nil, nil
}

func (h inputTestHelper) FindOutputClusterConnectionsFromRelease(context.Context, types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func (h inputTestHelper) FindConnectionsFromContract(ctx context.Context, namespace string, contract string) ([]kv1alpha1.Connection, ReconcileError) {
	list := &kv1alpha1.ConnectionList{}
	if err := h.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, NewReconcileError(err, false, "")
	}
	return list.Items, nil
}

func (h inputTestHelper) FindClusterConnectionsFromContract(context.Context, string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func testConnection(name, contract, uri string) *kv1alpha1.Connection {
	return &kv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: secretRefTestNamespace},
		Spec:       kv1alpha1.ConnectionSpec{Contract: contract, Values: &apiextensionsv1.JSON{Raw: []byte(`{"uri":"` + uri + `"}`)}},
		Status:     kv1alpha1.ConnectionStatus{Phase: kv1alpha1.ConnectionPhaseReady},
	}
}

func uri(v string) map[string]interface{} {
	return map[string]interface{}{"uri": v}
}

func buildInputs(t *testing.T, inputs, parameters string) (*BuildInputModelResult, ReconcileError) {
	t.Helper()
	pck := &kubopackage.Package{}
	manifest := "apiVersion: v1alpha1\nname: p\ntag: 1.0.0\nmodules: [{name: main, source: {oci: {repository: r, tag: t}}}]\n" +
		"inputs: " + inputs + "\n"
	if err := misc.YamlDecode(manifest, pck); err != nil {
		t.Fatal(err)
	}
	if err := pck.Groom(nil); err != nil {
		t.Fatal(err)
	}
	params := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(parameters), &params); err != nil {
		t.Fatal(err)
	}
	rendered, err := pck.RenderInputs(map[string]interface{}{"Parameters": params}, secretRefTestNamespace)
	if err != nil {
		t.Fatal(err)
	}
	cl := newSecretRefTestClient(t,
		testConnection("trino-a", "trino", "a"), testConnection("trino-b", "trino", "b"), testConnection("pg", "database-server", "pg"))
	return BuildInputModel(context.Background(), inputTestHelper{cl}, rendered)
}

func TestBuildInputModelFromParameter(t *testing.T) {
	result, rErr := buildInputs(t,
		"[{contract: trino, alias: trino, fromParameter: 'datasources[].trino', optional: true}, "+
			"{contract: database-server, alias: db, fromParameter: metadataDb}]",
		"{metadataDb: pg, datasources: [{trino: trino-b}, {name: none}, {trino: trino-a}]}")
	if rErr != nil {
		t.Fatal(rErr)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("messages: %v", result.Messages)
	}
	want := []interface{}{uri("b"), nil, uri("a")}
	if !reflect.DeepEqual(result.InputListModel["trino"], want) {
		t.Errorf("list: %v", result.InputListModel["trino"])
	}
	if _, ok := result.InputModel["trino"]; ok {
		t.Error("an item went to .Inputs")
	}
	if !reflect.DeepEqual(result.InputModel["db"], uri("pg")) {
		t.Errorf("single: %v", result.InputModel["db"])
	}
	if result.EffectiveInputConnections[1].Name != "trino-b" || result.EffectiveInputConnections[2].Name != "" {
		t.Errorf("effective: %v", result.EffectiveInputConnections)
	}
}

func TestBuildInputModelWrongContract(t *testing.T) {
	_, rErr := buildInputs(t, "[{contract: trino, fromParameter: 'datasources[].trino'}]", "{datasources: [{trino: pg}]}")
	if rErr == nil || !strings.Contains(rErr.Error(), "Contract mismatch") {
		t.Errorf("got %v", rErr)
	}
}

func TestBuildInputModelMissingItem(t *testing.T) {
	result, rErr := buildInputs(t, "[{contract: trino, fromParameter: 'datasources[].trino'}]", "{datasources: [{trino: trino-c}]}")
	if rErr != nil || len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "trino-c") {
		t.Errorf("got %v, %v", result, rErr)
	}
}

func TestBuildInputModelEmptyOptionalBindsNothing(t *testing.T) {
	result, rErr := buildInputs(t, `[{contract: trino, optional: true, connectionRef: {name: "{{ .Parameters.t }}"}}]`, "t: ''")
	if rErr != nil || len(result.InputModel) != 0 || len(result.WatchedInputConnections) != 0 {
		t.Errorf("got %+v, %v", result, rErr)
	}
}
