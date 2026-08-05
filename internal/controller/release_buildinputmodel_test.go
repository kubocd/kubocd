package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"strings"
	"testing"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// bimTestHelper wraps a fake client. The Find* methods are not exercised by
// the named-connection paths, they return empty sets.
type bimTestHelper struct {
	client.Client
}

func (h *bimTestHelper) FindOutputConnectionsFromRelease(_ context.Context, _ types.NamespacedName) ([]kv1alpha1.Connection, ReconcileError) {
	return nil, nil
}
func (h *bimTestHelper) FindOutputClusterConnectionsFromRelease(_ context.Context, _ types.NamespacedName) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}
func (h *bimTestHelper) FindConnectionsFromInterface(_ context.Context, _ string, _ string) ([]kv1alpha1.Connection, ReconcileError) {
	return nil, nil
}
func (h *bimTestHelper) FindClusterConnectionsFromInterface(_ context.Context, _ string) ([]kv1alpha1.ClusterConnection, ReconcileError) {
	return nil, nil
}

func bimScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	sch := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	if err := kv1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	return sch
}

func readyConnection(name, namespace, iface string, valuesJSON string) *kv1alpha1.Connection {
	c := &kv1alpha1.Connection{}
	c.Name = name
	c.Namespace = namespace
	c.Spec.Interface = iface
	c.Spec.Values = &v1.JSON{Raw: []byte(valuesJSON)}
	c.Status.Phase = kv1alpha1.ConnectionPhaseReady
	return c
}

func readyClusterConnection(name, iface string, valuesJSON string) *kv1alpha1.ClusterConnection {
	c := &kv1alpha1.ClusterConnection{}
	c.Name = name
	c.Spec.Interface = iface
	c.Spec.Values = &v1.JSON{Raw: []byte(valuesJSON)}
	c.Status.Phase = kv1alpha1.ConnectionPhaseReady
	return c
}

func namedInput(iface, alias, name, namespace string, kind kv1alpha1.Kind) kubopackage.InputRendered {
	var ir kubopackage.InputRendered
	ir.Interface = iface
	ir.Alias = alias
	ir.Kind = kind
	ir.NamedConnection.Name = name
	ir.NamedConnection.Namespace = namespace
	return ir
}

// The facade fix: a named namespaced Connection must be found through the
// Connection lookup (it used to instantiate the wrong facade type).
func TestNamedConnectionResolves(t *testing.T) {
	cnx := readyConnection("my-db", "okdp", "database-server", `{"host":"pg.okdp.svc","port":5432}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(cnx).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "my-db", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected no waiting message, got %v", result.Messages)
	}
	values, ok := result.InputModel["db"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected resolved values under alias 'db', got %#v", result.InputModel)
	}
	if values["host"] != "pg.okdp.svc" {
		t.Errorf("unexpected host: %v", values["host"])
	}
	eff := result.EffectiveInputConnections[0]
	if eff.Kind != kv1alpha1.KindConnection || eff.Name != "my-db" || eff.Namespace != "okdp" {
		t.Errorf("unexpected effective connection: %+v", eff)
	}
}

// A named ClusterConnection (explicit kind) is looked up by name only.
func TestNamedClusterConnectionResolves(t *testing.T) {
	cnx := readyClusterConnection("corporate-sso", "oidc", `{"issuerUri":"https://sso.corp"}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(cnx).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("oidc", "oidc", "corporate-sso", "", kv1alpha1.KindClusterConnection)}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	values, ok := result.InputModel["oidc"].(map[string]interface{})
	if !ok || values["issuerUri"] != "https://sso.corp" {
		t.Fatalf("expected resolved oidc values, got %#v", result.InputModel)
	}
	if result.EffectiveInputConnections[0].Kind != kv1alpha1.KindClusterConnection {
		t.Errorf("unexpected effective kind: %+v", result.EffectiveInputConnections[0])
	}
}

// An absent named connection gates (message) and registers watch entries with
// the CORRECT kinds so that its creation wakes the release up.
func TestNamedConnectionAbsentIsWatched(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "my-db", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "Waiting for namedConnection") {
		t.Fatalf("expected a waiting message, got %v", result.Messages)
	}
	var haveNamespaced, haveCluster bool
	for _, w := range result.WatchedInputConnections {
		if w.Kind == kv1alpha1.KindConnection && w.Name == "my-db" && w.Namespace == "okdp" {
			haveNamespaced = true
		}
		if w.Kind == kv1alpha1.KindClusterConnection && w.Name == "my-db" && w.Namespace == "" {
			haveCluster = true
		}
	}
	if !haveNamespaced || !haveCluster {
		t.Errorf("expected both watch entries with correct kinds, got %+v", result.WatchedInputConnections)
	}
}

// A named connection carrying another interface is a hard error.
func TestNamedConnectionInterfaceMismatch(t *testing.T) {
	cnx := readyConnection("my-db", "okdp", "s3", `{}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(cnx).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "my-db", "okdp", "")}
	_, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr == nil {
		t.Fatal("expected an interface mismatch error")
	}
}

// Election ordering: priority descending, then name ascending.
func TestElectionOrdering(t *testing.T) {
	facades := []kv1alpha1.ConnectionFacade{
		readyConnection("bbb", "okdp", "database-server", `{"id":"bbb"}`),
		readyConnection("aaa", "okdp", "database-server", `{"id":"aaa"}`),
		func() *kv1alpha1.Connection {
			c := readyConnection("low", "okdp", "database-server", `{"id":"low"}`)
			c.Spec.Priority = -5
			return c
		}(),
		func() *kv1alpha1.Connection {
			c := readyConnection("high", "okdp", "database-server", `{"id":"high"}`)
			c.Spec.Priority = 500
			return c
		}(),
	}
	var ir kubopackage.InputRendered
	ir.Interface = "database-server"
	ir.Alias = "dbs"
	ir.AllowMultiple = true

	collector := &BuildInputModelResult{
		InputModel:                make(map[string]interface{}),
		InputListModel:            make(map[string]interface{}),
		WatchedInputConnections:   []kv1alpha1.InputConnectionReference{},
		EffectiveInputConnections: make([]kv1alpha1.InputConnectionReference, 1),
	}
	if err := bimFilterConnection(facades, 0, ir, collector); err != nil {
		t.Fatalf("bimFilterConnection failed: %v", err)
	}
	list, ok := collector.InputListModel["dbs"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected a list under alias 'dbs', got %#v", collector.InputListModel)
	}
	got := make([]string, len(list))
	for i, v := range list {
		got[i] = v["id"].(string)
	}
	want := []string{"high", "aaa", "bbb", "low"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected ordering: got %v want %v", got, want)
		}
	}
	if collector.EffectiveInputConnections[0].Name != "high" {
		t.Errorf("elected should be 'high', got %+v", collector.EffectiveInputConnections[0])
	}
}
