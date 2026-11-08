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

//nolint:goconst // Keep claim cases readable.
package controller

import (
	"context"
	"errors"
	kv1alpha1 "kubocd/api/v1alpha1"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const claimConnectionSchema = `{"properties": {
	"engine": {"type": "string"},
	"serverVersion": {"type": "string", "default": "16"},
	"extensions": {"type": "array", "items": {"type": "string"}}}}`

func claimFixtures() (*kv1alpha1.ClusterContract, *kv1alpha1.Connection, *kv1alpha1.Claim) {
	contract := claimContract(databaseRequirements, databaseChecks)
	contract.Name = "database-v1"
	contract.Spec.Schema = &apiextensionsv1.JSON{Raw: []byte(claimConnectionSchema)}
	contract.Status.Phase = kv1alpha1.ContractPhaseReady
	connection := &kv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "ext-pg", Namespace: "demo"},
		Spec: kv1alpha1.ConnectionSpec{Contract: "database-v1",
			Values: &apiextensionsv1.JSON{Raw: []byte(`{"engine": "postgresql", "extensions": ["plpgsql"]}`)}},
		Status: kv1alpha1.ConnectionStatus{Phase: kv1alpha1.ConnectionPhaseReady}}
	claim := &kv1alpha1.Claim{ObjectMeta: metav1.ObjectMeta{Name: "hive-db", Namespace: "demo", Generation: 3},
		Spec: kv1alpha1.ClaimSpec{Contract: "database-v1", Target: kv1alpha1.ClaimTarget{Connection: "ext-pg"},
			Requirements: &apiextensionsv1.JSON{Raw: []byte(`{"engines": ["postgresql"], "serverVersion": ">=15", "extensions": ["plpgsql"]}`)}}}
	return contract, connection, claim
}

// reconcileClaim reconciles the claim among objects and returns it as stored afterward.
func reconcileClaim(t *testing.T, k8sClient client.Client, recorder record.EventRecorder, claim *kv1alpha1.Claim) (*kv1alpha1.Claim, error) {
	t.Helper()
	reconciler := &ClaimReconciler{Client: k8sClient, EventRecorder: recorder, Logger: logr.Discard()}
	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
	got := &kv1alpha1.Claim{}
	if getErr := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(claim), got); getErr != nil {
		t.Fatal(getErr)
	}
	return got, err
}

func claimClient(t *testing.T, funcs *interceptor.Funcs, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&kv1alpha1.Claim{})
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	return builder.Build()
}

func TestAClaimOnAConnectionIsBoundWhenTheConnectionMeetsItsRequirements(t *testing.T) {
	cases := map[string]struct {
		change      func(*kv1alpha1.ClusterContract, *kv1alpha1.Connection, *kv1alpha1.Claim)
		withoutCtrt bool
		want        string
	}{
		"met, with a default of the schema": {func(*kv1alpha1.ClusterContract, *kv1alpha1.Connection, *kv1alpha1.Claim) {}, false, ""},
		"no requirement": {func(_ *kv1alpha1.ClusterContract, _ *kv1alpha1.Connection, claim *kv1alpha1.Claim) {
			claim.Spec.Requirements = nil
		}, false, ""},
		"no contract and no requirement": {func(_ *kv1alpha1.ClusterContract, connection *kv1alpha1.Connection, claim *kv1alpha1.Claim) {
			connection.Spec.Values = nil
			claim.Spec.Requirements = nil
		}, true, ""},
		"unmet requirement": {func(_ *kv1alpha1.ClusterContract, _ *kv1alpha1.Connection, claim *kv1alpha1.Claim) {
			claim.Spec.Requirements = &apiextensionsv1.JSON{Raw: []byte(`{"serverVersion": ">=17"}`)}
		}, false, "requirement 'serverVersion' is not met by connection 'ext-pg'"},
		"unknown requirement": {func(_ *kv1alpha1.ClusterContract, _ *kv1alpha1.Connection, claim *kv1alpha1.Claim) {
			claim.Spec.Requirements = &apiextensionsv1.JSON{Raw: []byte(`{"size": "10Gi"}`)}
		}, false, "invalid requirements: "},
		"no contract and a requirement": {func(_ *kv1alpha1.ClusterContract, connection *kv1alpha1.Connection, _ *kv1alpha1.Claim) {
			connection.Spec.Values = nil
		}, true, "invalid requirements: "},
		"missing connection": {func(_ *kv1alpha1.ClusterContract, _ *kv1alpha1.Connection, claim *kv1alpha1.Claim) {
			claim.Spec.Target.Connection = "other"
		}, false, "connection 'other' not found"},
		"other contract": {func(_ *kv1alpha1.ClusterContract, connection *kv1alpha1.Connection, _ *kv1alpha1.Claim) {
			connection.Spec.Contract = "s3-v1"
		}, false, "connection 'ext-pg' follows the contract 's3-v1', not 'database-v1'"},
		"connection not ready": {func(_ *kv1alpha1.ClusterContract, connection *kv1alpha1.Connection, _ *kv1alpha1.Claim) {
			connection.Status.Phase = kv1alpha1.ConnectionPhaseError
		}, false, "connection 'ext-pg' is not ready"},
		"contract not ready": {func(contract *kv1alpha1.ClusterContract, _ *kv1alpha1.Connection, _ *kv1alpha1.Claim) {
			contract.Status.Phase = kv1alpha1.ContractPhaseError
		}, false, "contract 'database-v1' is not ready"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			contract, connection, claim := claimFixtures()
			c.change(contract, connection, claim)
			objects := []client.Object{connection, claim}
			if !c.withoutCtrt {
				objects = append(objects, contract)
			}
			recorder := record.NewFakeRecorder(10)
			got, err := reconcileClaim(t, claimClient(t, nil, objects...), recorder, claim)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status.ObservedGeneration != 3 {
				t.Errorf("observedGeneration: got %d, want 3", got.Status.ObservedGeneration)
			}
			if c.want == "" {
				if got.Status.Phase != kv1alpha1.ClaimPhaseBound || got.Status.Connection != "ext-pg" || got.Status.Message != "" {
					t.Fatalf("got status %+v, want BOUND to ext-pg", got.Status)
				}
				return
			}
			if got.Status.Phase != kv1alpha1.ClaimPhaseUnbound || got.Status.Connection != "" || !strings.HasPrefix(got.Status.Message, c.want) {
				t.Fatalf("got status %+v, want UNBOUND with %q", got.Status, c.want)
			}
			if strings.Contains(got.Status.Message, "16") {
				t.Errorf("the message quotes a value of the connection: %q", got.Status.Message)
			}
			if event := <-recorder.Events; !strings.HasPrefix(event, "Warning Unbound "+c.want) {
				t.Errorf("got event %q", event)
			}
		})
	}
}

func TestAClaimWhoseStatusIsUpToDateIsNotWrittenAgain(t *testing.T) {
	contract, connection, claim := claimFixtures()
	writes := 0
	funcs := &interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		writes++
		return c.SubResource(subResource).Patch(ctx, obj, patch, opts...)
	}}
	k8sClient := claimClient(t, funcs, contract, connection, claim)
	recorder := record.NewFakeRecorder(10)
	for range 2 {
		if _, err := reconcileClaim(t, k8sClient, recorder, claim); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 || len(recorder.Events) != 1 {
		t.Fatalf("got %d status writes and %d events, want 1 and 1", writes, len(recorder.Events))
	}
}

func TestAClaimOnAProvisionerKeepsTheStatusOfTheProvisioner(t *testing.T) {
	contract, connection, claim := claimFixtures()
	claim.Spec.Target = kv1alpha1.ClaimTarget{Provisioner: "postgresql"}
	claim.Status = kv1alpha1.ClaimStatus{Phase: kv1alpha1.ClaimPhaseUnbound, Message: "creating the database", ObservedGeneration: 2}
	recorder := record.NewFakeRecorder(10)
	got, err := reconcileClaim(t, claimClient(t, nil, contract, connection, claim), recorder, claim)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != claim.Status || len(recorder.Events) != 0 {
		t.Fatalf("got status %+v and %d events, want %+v and none", got.Status, len(recorder.Events), claim.Status)
	}
}

func TestARefusedReadOfTheConnectionOfAClaimIsReturned(t *testing.T) {
	contract, connection, claim := claimFixtures()
	funcs := &interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*kv1alpha1.Connection); ok {
			return apierrors.NewForbidden(schema.GroupResource{Group: "kubocd.kubotal.io", Resource: "connections"}, key.Name, errors.New("denied"))
		}
		return c.Get(ctx, key, obj, opts...)
	}}
	got, err := reconcileClaim(t, claimClient(t, funcs, contract, connection, claim), record.NewFakeRecorder(10), claim)
	if err == nil || !strings.HasPrefix(err.Error(), "could not get the Connection: the API server answered Forbidden") {
		t.Fatalf("got error %v", err)
	}
	if got.Status.Phase != "" {
		t.Fatalf("got status %+v, want none", got.Status)
	}
}
