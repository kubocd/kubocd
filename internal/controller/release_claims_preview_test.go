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
package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kuboschema"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func previewClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	contract := &kv1alpha1.ClusterContract{ObjectMeta: metav1.ObjectMeta{Name: "s3-v1"},
		Spec: kv1alpha1.ClusterContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(`{"properties": {
			"endpoint": {"type": "string", "required": true},
			"region": {"type": "string", "default": "us-east-1"},
			"buckets": {"type": "array", "items": {"type": "string"}},
			"secretRef": {"type": "string", "required": true},
			"pathStyle": {"type": "boolean"}}}`)}}}
	store := &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: "kubocd-system"}}
	store.Status.Provisioners = []kv1alpha1.ReleaseProvisioner{{Name: "storage", Contract: "s3-v1", Namespaces: []string{"apps"},
		Values: &apiextensionsv1.JSON{Raw: []byte(`{"endpoint": "http://storage:9000", "pathStyle": true}`)}}}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(contract, store).Build()
}

var previewDecls = []kuboschema.ClaimDecl{{Path: []string{"storage"}, Contract: "s3-v1", Required: true}}

func TestAClaimOnAProvisionerGetsWhatItAnnouncesAndPlaceholders(t *testing.T) {
	release := &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "hive", Namespace: "apps"}}
	parameters := map[string]interface{}{"storage": map[string]interface{}{"provisioner": "storage"}}

	got, err := PreviewClaims(context.Background(), previewClient(t), release, previewDecls, parameters, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"endpoint":  "http://storage:9000",
		"pathStyle": true,
		"region":    "us-east-1",
		"buckets":   []interface{}{"<set by provisioner storage>"},
		"secretRef": "<set by provisioner storage>",
	}
	if !reflect.DeepEqual(got.Values["parameters.storage"], want) {
		t.Fatalf("got values %v", got.Values["parameters.storage"])
	}
	if p := got.Previews[0]; p.Claim != "hive-storage" || p.Provisioner != "storage" ||
		!reflect.DeepEqual(p.Placeholders, []string{"buckets", "secretRef"}) {
		t.Fatalf("got preview %+v", p)
	}
	if len(got.Inputs) != 0 || len(got.Bindings) != 1 {
		t.Fatalf("a claim on a provisioner binds its values, not an input: %+v", got)
	}
}

func TestARequirementNamedLikeAPropertyGivesItsValue(t *testing.T) {
	release := &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "hive", Namespace: "apps"}}
	parameters := map[string]interface{}{"storage": map[string]interface{}{"provisioner": "storage",
		"requirements": map[string]interface{}{"buckets": []interface{}{"lake"}}}}

	got, err := PreviewClaims(context.Background(), previewClient(t), release, previewDecls, parameters, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := got.Values["parameters.storage"].(map[string]interface{})
	if !reflect.DeepEqual(values["buckets"], []interface{}{"lake"}) {
		t.Fatalf("got buckets %v", values["buckets"])
	}
	if !reflect.DeepEqual(got.Previews[0].Placeholders, []string{"secretRef"}) {
		t.Fatalf("got placeholders %v", got.Previews[0].Placeholders)
	}
}

func TestAClaimOnAConnectionIsAnInputLikeAConnectionRef(t *testing.T) {
	release := &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "hive", Namespace: "apps"}}
	parameters := map[string]interface{}{"storage": map[string]interface{}{"connection": "lake"}}

	got, err := PreviewClaims(context.Background(), previewClient(t), release, previewDecls, parameters, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inputs) != 1 || got.Inputs[0].ConnectionRef.Name != "lake" || got.Inputs[0].ConnectionRef.Namespace != "apps" ||
		got.Inputs[0].Alias != "parameters.storage" || len(got.Values) != 0 || got.Previews[0].Connection != "lake" {
		t.Fatalf("got %+v", got)
	}
}

func TestAClaimOnAProvisionerThatDoesNotServeTheNamespaceFails(t *testing.T) {
	release := &kv1alpha1.Release{ObjectMeta: metav1.ObjectMeta{Name: "hive", Namespace: "other"}}
	parameters := map[string]interface{}{"storage": map[string]interface{}{"provisioner": "storage"}}

	_, err := PreviewClaims(context.Background(), previewClient(t), release, previewDecls, parameters, nil)
	if err == nil || !strings.Contains(err.Error(), "claim 'hive-storage': no provisioner 'storage' of contract 's3-v1' serves namespace 'other'") {
		t.Fatalf("got %v", err)
	}
}
