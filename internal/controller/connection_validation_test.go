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
	"errors"
	"fmt"
	kv1alpha1 "kubocd/api/v1alpha1"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// hostSchema is a contract schema that declares host only.
const hostSchema = `{"properties":{"host":{"type":"string"}}}`

// databaseContract is the contract of the outputs.
const databaseContract = "database"

func TestConnectionValidationErrorsDoNotQuoteTheValues(t *testing.T) {
	cases := map[string]struct {
		schema string
		values string
		want   string
	}{
		"additional property named from the data": {hostSchema, `{"host":"pg","` + canaryPassword + `":"x"}`,
			"validation error on values: (root): a property is not declared in the schema"},
		"enum value from the data": {`{"properties":{"mode":{"type":"string","enum":["fast","safe"]}}}`, `{"mode":"` + canaryPassword + `"}`,
			`validation error on values: mode: must be one of the following: "fast", "safe"`},
		"pattern value from the data": {`{"properties":{"host":{"type":"string","pattern":"^pg"}}}`, `{"host":"` + canaryPassword + `"}`,
			"validation error on values: host: Does not match pattern '^pg'"},
		"enum value under a property named from the data": {`{"properties":{},"patternProperties":{"^s3":{"enum":["fast"]}}}`, `{"` + canaryPassword + `":"slow"}`,
			`validation error on values: (root): must be one of the following: "fast"`},
		"pattern value under a property named from the data": {`{"properties":{},"patternProperties":{"^s3":{"type":"string","pattern":"^pg"}}}`,
			`{"` + canaryPassword + `":"x"}`, "validation error on values: (root): Does not match pattern '^pg'"},
		"key twice": {hostSchema, `{"` + canaryPassword + `":1,"` + canaryPassword + `":2}`,
			"unable to parse connection values: they are not a map with distinct keys"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			contract := &kv1alpha1.Contract{
				Spec:   kv1alpha1.ContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(c.schema)}},
				Status: kv1alpha1.ContractStatus{Phase: kv1alpha1.ContractPhaseReady},
			}
			err := checkConnection(contract, readyConnection("db", databaseContract, c.values))
			if err != nil && strings.Contains(err.Error(), canaryPassword[:7]) {
				t.Errorf("the error quotes the data: %s", err)
			}
			if err == nil || err.Error() != c.want {
				t.Fatalf("got error %v, want %q", err, c.want)
			}
		})
	}
}

// reconciledConnection is what one reconciliation of a connection left
// behind, in the sinks of a Release.
type reconciledConnection = reconciledRelease

// reconcileConnection reconciles once the connection of the kind given, on
// the API server of k8sClient.
func reconcileConnection(t *testing.T, k8sClient client.WithWatch, kind kv1alpha1.Kind, key types.NamespacedName) reconciledConnection {
	t.Helper()
	log := &logBuffer{}
	recorder := record.NewFakeRecorder(100)
	logger := funcr.New(log.write, funcr.Options{Verbosity: 2})
	request := ctrl.Request{NamespacedName: key}
	result := reconciledConnection{}
	if kind == kv1alpha1.KindClusterConnection {
		reconciler := &ClusterConnectionReconciler{Client: k8sClient, EventRecorder: recorder, Logger: logger}
		_, result.err = reconciler.Reconcile(context.Background(), request)
		clusterConnection := &kv1alpha1.ClusterConnection{}
		if err := k8sClient.Get(context.Background(), key, clusterConnection); err != nil {
			t.Fatal(err)
		}
		result.status = clusterConnection.Status.Message
	} else {
		reconciler := &ConnectionReconciler{Client: k8sClient, EventRecorder: recorder, Logger: logger}
		_, result.err = reconciler.Reconcile(context.Background(), request)
		connection := &kv1alpha1.Connection{}
		if err := k8sClient.Get(context.Background(), key, connection); err != nil {
			t.Fatal(err)
		}
		result.status = connection.Status.Message
	}
	result.log = log.buf.String()
	for len(recorder.Events) > 0 {
		result.events = append(result.events, <-recorder.Events)
	}
	return result
}

// outputPackage has one output, of the kind and the contract given, whose
// values add the property given to host.
const outputPackage = `outputs: |
  - contract: %s
    kind: %s
    name: db
    values:
      host: pg%s`

// The Connection controller and the Release controller are not run by the
// envtest suite: the test chains their reconciliations on one fake client.
func TestAContractRefusalReachesTheReleaseWithoutTheValues(t *testing.T) {
	const (
		propertyFromTheContext = "\n      \"{{ .Context.db.password }}\": x"
		contractFromTheContext = `"{{ .Context.db.password }}"`
		notDeclared            = "validation error on values: (root): a property is not declared in the schema"
	)
	contract := &kv1alpha1.Contract{
		ObjectMeta: metav1.ObjectMeta{Name: databaseContract, Namespace: "apps"},
		Spec:       kv1alpha1.ContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(hostSchema)}},
		Status:     kv1alpha1.ContractStatus{Phase: kv1alpha1.ContractPhaseReady},
	}
	clusterContract := &kv1alpha1.ClusterContract{
		ObjectMeta: metav1.ObjectMeta{Name: databaseContract},
		Spec:       kv1alpha1.ClusterContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(hostSchema)}},
		Status:     kv1alpha1.ClusterContractStatus{Phase: kv1alpha1.ContractPhaseReady},
	}
	connectionKey := types.NamespacedName{Namespace: "apps", Name: BuildConnectionName("demo", "db")}
	clusterConnectionKey := types.NamespacedName{Name: BuildClusterConnectionName("demo", "apps", "db")}
	cases := map[string]struct {
		kind     kv1alpha1.Kind
		contract string
		property string
		objects  []client.Object
		key      types.NamespacedName
		want     string
	}{
		"Connection refused by its Contract": {kv1alpha1.KindConnection, databaseContract, propertyFromTheContext,
			[]client.Object{contract}, connectionKey, notDeclared},
		"Connection whose contract is missing": {kv1alpha1.KindConnection, contractFromTheContext, "",
			nil, connectionKey, "the Contract or ClusterContract named in spec.contract is missing"},
		"ClusterConnection refused by its ClusterContract": {kv1alpha1.KindClusterConnection, databaseContract, propertyFromTheContext,
			[]client.Object{clusterContract}, clusterConnectionKey, notDeclared},
		"ClusterConnection whose ClusterContract is missing": {kv1alpha1.KindClusterConnection, contractFromTheContext, "",
			nil, clusterConnectionKey, "the ClusterContract named in spec.contract is missing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			objects := make([]client.Object, 0, 1+len(c.objects))
			objects = append(objects, readyHelmRelease())
			for _, object := range c.objects {
				objects = append(objects, object.DeepCopyObject().(client.Object))
			}
			manifest := fmt.Sprintf(afterRenderPackage, fmt.Sprintf(outputPackage, c.contract, c.kind, c.property), "")
			testbed := newReleaseTestbed(t, manifest, "", afterRenderContext, withObjects(t, objects...))
			if created := testbed.reconcile(t); created.err != nil {
				t.Fatalf("the output was not created: %v", created.err)
			}
			connection := reconcileConnection(t, testbed.client, c.kind, c.key)
			release := testbed.reconcile(t)
			for sink, text := range connection.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s of the %s quotes the data:\n%s", sink, c.kind, text)
				}
			}
			for sink, text := range release.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s of the Release quotes the data:\n%s", sink, text)
				}
			}
			if !strings.Contains(connection.status, c.want) ||
				!slices.ContainsFunc(connection.events, func(event string) bool { return strings.Contains(event, c.want) }) ||
				connection.err == nil || !strings.Contains(connection.err.Error(), c.want) {
				t.Fatalf("got status %q, events %q and error %v of the %s, want them to hold %q", connection.status, connection.events, connection.err, c.kind, c.want)
			}
			release.mustReport(t, c.want)
			if want := "Connection 'db': " + c.want; release.status != want {
				t.Fatalf("got status %q of the Release, want %q", release.status, want)
			}
		})
	}
}

// refusingCanaryRequests refuses, as the API server would, to read a contract
// of the kinds given whose name holds the canary, quoting the name, and, as an
// admission webhook would, a status update of a connection whose values hold
// the canary, quoting the values.
func refusingCanaryRequests(base client.WithWatch, contractKinds ...string) client.WithWatch {
	return interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			gvk, err := apiutil.GVKForObject(obj, c.Scheme())
			if err != nil {
				return err
			}
			if slices.Contains(contractKinds, gvk.Kind) && strings.Contains(key.Name, canaryPassword[:7]) {
				resource := schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}
				return apierrors.NewForbidden(resource, key.Name, errors.New("access denied"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch,
			opts ...client.SubResourcePatchOption) error {
			connection, ok := obj.(kv1alpha1.ConnectionFacade)
			if ok && strings.Contains(string(connection.GetValuesRaw()), canaryPassword[:7]) {
				gvk, err := apiutil.GVKForObject(obj, c.Scheme())
				if err != nil {
					return err
				}
				resource := schema.GroupResource{Group: gvk.Group, Resource: strings.ToLower(gvk.Kind) + "s"}
				return apierrors.NewForbidden(resource, obj.GetName(),
					fmt.Errorf("admission webhook \"policy.example.com\" denied the request: the values %s are protected", connection.GetValuesRaw()))
			}
			return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
	})
}

func TestARefusedRequestOfTheConnectionControllersIsReturnedWithoutTheAnswer(t *testing.T) {
	values := &apiextensionsv1.JSON{Raw: []byte(`{"host":"pg","` + canaryPassword + `":"x"}`)}
	connection := func(contract string) client.Object {
		return &kv1alpha1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"},
			Spec: kv1alpha1.ConnectionSpec{Contract: contract, Values: values}}
	}
	clusterConnection := func(contract string) client.Object {
		return &kv1alpha1.ClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "db"},
			Spec: kv1alpha1.ClusterConnectionSpec{Contract: contract, Values: values}}
	}
	contract := &kv1alpha1.Contract{ObjectMeta: metav1.ObjectMeta{Name: databaseContract, Namespace: "apps"},
		Spec:   kv1alpha1.ContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(hostSchema)}},
		Status: kv1alpha1.ContractStatus{Phase: kv1alpha1.ContractPhaseReady}}
	clusterContract := &kv1alpha1.ClusterContract{ObjectMeta: metav1.ObjectMeta{Name: databaseContract},
		Spec:   kv1alpha1.ClusterContractSpec{Schema: &apiextensionsv1.JSON{Raw: []byte(hostSchema)}},
		Status: kv1alpha1.ClusterContractStatus{Phase: kv1alpha1.ContractPhaseReady}}
	const answered = "the API server answered Forbidden, " + objectWithheld
	cases := map[string]struct {
		kind    kv1alpha1.Kind
		objects []client.Object
		refused []string
		want    string
	}{
		"Contract of a Connection": {kv1alpha1.KindConnection, []client.Object{connection(canaryPassword)},
			[]string{"Contract"}, "could not get the Contract: " + answered},
		"ClusterContract of a Connection": {kv1alpha1.KindConnection, []client.Object{connection(canaryPassword)},
			[]string{"ClusterContract"}, "could not get the ClusterContract: " + answered},
		"ClusterContract of a ClusterConnection": {kv1alpha1.KindClusterConnection, []client.Object{clusterConnection(canaryPassword)},
			[]string{"ClusterContract"}, "could not get the ClusterContract: " + answered},
		"status of a Connection": {kv1alpha1.KindConnection, []client.Object{connection(databaseContract), contract},
			nil, "could not update the Connection status: " + answered},
		"status of a ClusterConnection": {kv1alpha1.KindClusterConnection, []client.Object{clusterConnection(databaseContract), clusterContract},
			nil, "could not update the ClusterConnection status: " + answered},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			objects := make([]client.Object, 0, len(c.objects))
			for _, object := range c.objects {
				objects = append(objects, object.DeepCopyObject().(client.Object))
			}
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
				WithStatusSubresource(&kv1alpha1.Connection{}, &kv1alpha1.ClusterConnection{}).Build()
			got := reconcileConnection(t, refusingCanaryRequests(base, c.refused...), c.kind, client.ObjectKeyFromObject(c.objects[0]))
			for sink, text := range got.sinks() {
				if strings.Contains(text, canaryPassword[:7]) {
					t.Errorf("the %s quotes the data:\n%s", sink, text)
				}
			}
			if got.err == nil || got.err.Error() != c.want {
				t.Fatalf("got returned error %v, want %q", got.err, c.want)
			}
		})
	}
}
