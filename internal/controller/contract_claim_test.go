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

//nolint:goconst // Keep requirement cases readable.
package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAnInvalidClaimSectionPutsTheContractInError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	claim := &kv1alpha1.ContractClaim{Checks: map[string]string{"dbName": "true"}}
	contract := &kv1alpha1.Contract{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"}, Spec: kv1alpha1.ContractSpec{Claim: claim}}
	clusterContract := &kv1alpha1.ClusterContract{ObjectMeta: metav1.ObjectMeta{Name: "db"}, Spec: kv1alpha1.ClusterContractSpec{Claim: claim}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(contract, clusterContract).
		WithStatusSubresource(&kv1alpha1.Contract{}, &kv1alpha1.ClusterContract{}).Build()
	recorder := record.NewFakeRecorder(10)
	ctx := context.Background()
	if _, err := (&ContractReconciler{Client: k8sClient, EventRecorder: recorder, Logger: logr.Discard()}).
		Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(contract)}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ClusterContractReconciler{Client: k8sClient, EventRecorder: recorder, Logger: logr.Discard()}).
		Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(clusterContract)}); err != nil {
		t.Fatal(err)
	}
	gotContract, gotClusterContract := &kv1alpha1.Contract{}, &kv1alpha1.ClusterContract{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(contract), gotContract); err != nil {
		t.Fatal(err)
	}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterContract), gotClusterContract); err != nil {
		t.Fatal(err)
	}
	for kind, status := range map[string]kv1alpha1.ContractStatus{
		"Contract": gotContract.Status, "ClusterContract": kv1alpha1.ContractStatus(gotClusterContract.Status),
	} {
		if status.Phase != kv1alpha1.ContractPhaseError || !strings.Contains(status.Message, "the check 'dbName' names no requirement") {
			t.Errorf("%s: got phase %q, message %q", kind, status.Phase, status.Message)
		}
	}
}

func TestVersionSatisfies(t *testing.T) {
	for _, tc := range []struct {
		version    string
		constraint string
		want       bool
		wantErr    bool
	}{
		{"18", ">=15", true, false},
		{"18", ">=15, <18", false, false},
		{"17.2", ">=15, <18", true, false},
		{"15", ">15", false, false},
		{"15.0.1", ">15", true, false},
		{"3.5", ">=3.5.0", true, false},
		{"3.5", "<3.5.1", true, false},
		{"18", "=18.0.0", true, false},
		{"18", "!=18", false, false},
		{"18", "!=17", true, false},
		{"16", "<= 16 , >= 16", true, false},
		{"16beta", ">=15", false, true},
		{"", ">=15", false, true},
		{"16", "15", false, true},
		{"16", "", false, true},
		{"16", ">=15,", false, true},
		{"16", ">=v15", false, true},
	} {
		got, err := versionSatisfies(tc.version, tc.constraint)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("versionSatisfies(%q, %q) = %v, %v; want %v, error %v", tc.version, tc.constraint, got, err, tc.want, tc.wantErr)
		}
	}
}

const databaseRequirements = `{"properties": {
	"engines": {"type": "array", "items": {"type": "string"}},
	"serverVersion": {"type": "string"},
	"extensions": {"type": "array", "items": {"type": "string"}},
	"dbName": {"type": "string"}}}`

var databaseChecks = map[string]string{
	"engines":       "connection.engine in claim.engines",
	"serverVersion": "versionSatisfies(connection.serverVersion, claim.serverVersion)",
	"extensions":    "claim.extensions.all(e, e in connection.extensions)",
	"dbName":        "connection.dbName == claim.dbName",
}

func claimContract(requirements string, checks map[string]string) *kv1alpha1.ClusterContract {
	contract := &kv1alpha1.ClusterContract{}
	contract.Spec.Claim = &kv1alpha1.ContractClaim{Checks: checks}
	if requirements != "" {
		contract.Spec.Claim.Requirements = &apiextensionsv1.JSON{Raw: []byte(requirements)}
	}
	return contract
}

func TestResolveContractClaim(t *testing.T) {
	if _, err := resolveContractClaim(&kv1alpha1.ClusterContract{}); err != nil {
		t.Fatalf("a contract without claim section: %v", err)
	}
	if _, err := resolveContractClaim(claimContract(databaseRequirements, databaseChecks)); err != nil {
		t.Fatalf("the database contract: %v", err)
	}
	for _, tc := range []struct {
		name         string
		requirements string
		checks       map[string]string
		wantErr      string
	}{
		{"missing check", `{"properties": {"engines": {"type": "array", "items": {"type": "string"}}, "dbName": {"type": "string"}}}`,
			map[string]string{"engines": "connection.engine in claim.engines"}, "the requirement 'dbName' has no check"},
		{"check without requirement", "", map[string]string{"engines": "true"}, "the check 'engines' names no requirement"},
		{"invalid expression", `{"properties": {"dbName": {"type": "string"}}}`, map[string]string{"dbName": "connection.dbName =="}, "the check 'dbName' is not valid"},
		{"not a boolean", `{"properties": {"dbName": {"type": "string"}}}`, map[string]string{"dbName": "'a' + 'b'"}, "does not evaluate to a boolean"},
		{"invalid schema", `{"properties": {"dbName": {"type": "strange"}}}`, map[string]string{"dbName": "true"}, "requirements schema conversion"},
	} {
		_, err := resolveContractClaim(claimContract(tc.requirements, tc.checks))
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: got %v, want an error containing %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestUnmetRequirement(t *testing.T) {
	claim, err := resolveContractClaim(claimContract(databaseRequirements, databaseChecks))
	if err != nil {
		t.Fatal(err)
	}
	connection := map[string]interface{}{
		"engine":        "postgresql",
		"serverVersion": "18",
		"dbName":        "demo_hive",
		"extensions":    []interface{}{"vector", "postgis"},
	}
	for _, tc := range []struct {
		name         string
		requirements map[string]interface{}
		connection   map[string]interface{}
		wantUnmet    string
		wantErr      string
	}{
		{"no requirement", nil, connection, "", ""},
		{"all met", map[string]interface{}{
			"engines": []interface{}{"postgresql"}, "serverVersion": ">=15", "extensions": []interface{}{"postgis"}, "dbName": "demo_hive",
		}, connection, "", ""},
		{"engine", map[string]interface{}{"engines": []interface{}{"mariadb"}}, connection, "engines", ""},
		{"version", map[string]interface{}{"serverVersion": ">=15, <18"}, connection, "serverVersion", ""},
		{"extension", map[string]interface{}{"extensions": []interface{}{"vector", "timescaledb"}}, connection, "extensions", ""},
		{"first by name", map[string]interface{}{"serverVersion": "<18", "dbName": "other"}, connection, "dbName", ""},
		{"empty property", map[string]interface{}{"serverVersion": ">=15"}, map[string]interface{}{"engine": "postgresql"}, "serverVersion", ""},
		{"invalid constraint", map[string]interface{}{"serverVersion": "15"}, connection, "serverVersion", ""},
		{"unknown requirement", map[string]interface{}{"size": "10Gi"}, connection, "", "invalid requirements"},
		{"wrong type", map[string]interface{}{"engines": "postgresql"}, connection, "", "invalid requirements"},
	} {
		unmet, err := claim.unmetRequirement(context.Background(), tc.requirements, tc.connection, DefaultConditionLimits)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: got %v, want an error containing %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || unmet != tc.wantUnmet {
			t.Errorf("%s: got %q, %v; want %q", tc.name, unmet, err, tc.wantUnmet)
		}
	}
}

func TestUnmetRequirementWithoutClaimSection(t *testing.T) {
	claim, err := resolveContractClaim(&kv1alpha1.ClusterContract{})
	if err != nil {
		t.Fatal(err)
	}
	if unmet, err := claim.unmetRequirement(context.Background(), nil, map[string]interface{}{}, DefaultConditionLimits); err != nil || unmet != "" {
		t.Fatalf("no requirement: got %q, %v", unmet, err)
	}
	if _, err := claim.unmetRequirement(context.Background(), map[string]interface{}{"dbName": "x"}, map[string]interface{}{}, DefaultConditionLimits); err == nil {
		t.Fatal("a requirement on a contract without claim section must be refused")
	}
}

func TestUnmetRequirementLimits(t *testing.T) {
	claim, err := resolveContractClaim(claimContract(`{"properties": {"engines": {"type": "array", "items": {"type": "string"}}}}`,
		map[string]string{"engines": "claim.engines.all(a, claim.engines.all(b, claim.engines.all(c, a + b + c != '')))"}))
	if err != nil {
		t.Fatal(err)
	}
	engines := make([]interface{}, 200)
	for i := range engines {
		engines[i] = "e"
	}
	_, err = claim.unmetRequirement(context.Background(), map[string]interface{}{"engines": engines}, map[string]interface{}{}, ConditionLimits{CostLimit: 1000})
	if err == nil || !strings.Contains(err.Error(), "cost limit exceeded") {
		t.Fatalf("got %v, want a cost limit error", err)
	}
}
