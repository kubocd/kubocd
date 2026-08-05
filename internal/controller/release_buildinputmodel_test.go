package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
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

// Dual lookup: a candidate carrying another interface is discarded, and with
// no valid alternative the release waits (no hard error loop).
func TestNamedConnectionInterfaceMismatchDualWaits(t *testing.T) {
	cnx := readyConnection("my-db", "okdp", "s3", `{}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(cnx).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "my-db", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("dual lookup must not hard-error on a mismatching candidate: %v", rerr)
	}
	if len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "Waiting for namedConnection") {
		t.Fatalf("expected a waiting message, got %v", result.Messages)
	}
}

// A cluster-scoped target has no namespace: the message must not show a
// dangling colon ("':shared-db'").
func TestWaitingMessageOnClusterConnectionHasNoDanglingColon(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "shared-db", "", kv1alpha1.KindClusterConnection)}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected one waiting message, got %v", result.Messages)
	}
	if !strings.Contains(result.Messages[0], "namedConnection 'shared-db'") {
		t.Fatalf("expected a bare name, got %q", result.Messages[0])
	}
	if strings.Contains(result.Messages[0], "':shared-db'") {
		t.Fatalf("dangling colon in %q", result.Messages[0])
	}
}

// With an explicit kind, an interface mismatch stays a hard error.
func TestNamedConnectionInterfaceMismatchExplicitKind(t *testing.T) {
	cnx := readyConnection("my-db", "okdp", "s3", `{}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(cnx).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "my-db", "okdp", kv1alpha1.KindConnection)}
	_, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr == nil {
		t.Fatal("expected an interface mismatch error with an explicit kind")
	}
}

// Dual lookup: a homonym Connection with the wrong interface must not mask a
// valid ClusterConnection.
func TestNamedConnectionDualPrefersMatchingKind(t *testing.T) {
	wrong := readyConnection("sso", "okdp", "s3", `{}`)
	right := readyClusterConnection("sso", "oidc", `{"issuerUri":"https://sso"}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(wrong, right).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("oidc", "oidc", "sso", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	values, ok := result.InputModel["oidc"].(map[string]interface{})
	if !ok || values["issuerUri"] != "https://sso" {
		t.Fatalf("the valid ClusterConnection should resolve, got %#v (messages %v)", result.InputModel, result.Messages)
	}
}

// The homonym deadlock a connectionRef 'kind' exists to break: a Connection
// and a ClusterConnection sharing the name AND the interface are both
// candidates of a dual lookup, and a generated ref never allows multiple.
func TestNamedConnectionHomonymNeedsKind(t *testing.T) {
	namespaced := readyConnection("shared-db", "okdp", "database-server", `{"host":"local"}`)
	cluster := readyClusterConnection("shared-db", "database-server", `{"host":"corporate"}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(namespaced, cluster).Build()
	helper := &bimTestHelper{Client: cl}

	// No kind: ambiguous, the release stays gated
	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "shared-db", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 1 || !strings.Contains(result.Messages[0], "Too many possible connections") {
		t.Fatalf("expected an ambiguity message, got %v", result.Messages)
	}
	// The message must tell the two candidates apart: they share one name, so
	// the kind (and the namespace of the namespaced one) has to be spelled out
	for _, want := range []string{"(db)", "Connection okdp:shared-db", "ClusterConnection shared-db"} {
		if !strings.Contains(result.Messages[0], want) {
			t.Fatalf("ambiguity message should contain %q, got %q", want, result.Messages[0])
		}
	}

	// kind: ClusterConnection picks the cluster-scoped one (no namespace)
	inputs = []kubopackage.InputRendered{namedInput("database-server", "db", "shared-db", "", kv1alpha1.KindClusterConnection)}
	result, rerr = BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	values, ok := result.InputModel["db"].(map[string]interface{})
	if !ok || values["host"] != "corporate" {
		t.Fatalf("kind: ClusterConnection should elect the cluster one, got %#v (messages %v)", result.InputModel, result.Messages)
	}

	// kind: Connection picks the namespaced one
	inputs = []kubopackage.InputRendered{namedInput("database-server", "db", "shared-db", "okdp", kv1alpha1.KindConnection)}
	result, rerr = BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	values, ok = result.InputModel["db"].(map[string]interface{})
	if !ok || values["host"] != "local" {
		t.Fatalf("kind: Connection should elect the namespaced one, got %#v (messages %v)", result.InputModel, result.Messages)
	}
}

// End to end on the generated path: a connectionRef declaring a kind carries
// it all the way from the package schema to the resolved values.
func TestGeneratedRefKindResolvesHomonym(t *testing.T) {
	namespaced := readyConnection("shared-db", "okdp", "database-server", `{"host":"local"}`)
	cluster := readyClusterConnection("shared-db", "database-server", `{"host":"corporate"}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(namespaced, cluster).Build()
	helper := &bimTestHelper{Client: cl}

	schema := kuboschema.KuboSchema{
		"properties": map[string]interface{}{
			"metadataDb": map[string]interface{}{
				"type":      kuboschema.TypeConnectionRef,
				"interface": "database-server",
				"kind":      "ClusterConnection",
				"required":  true,
			},
		},
	}
	openAPI, err := kuboschema.Kubo2openAPI(schema, false)
	if err != nil {
		t.Fatalf("Kubo2openAPI failed: %v", err)
	}
	decls, err := kuboschema.CollectConnectionDecls(openAPI, false)
	if err != nil {
		t.Fatalf("collect failed: %v", err)
	}
	params := map[string]interface{}{"metadataDb": "shared-db"}
	gen, bindings, err := GenerateRefInputs(decls, nil, params, nil, "okdp", nil)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	result, rerr := BuildInputModel(context.Background(), helper, gen)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("expected no gating message, got %v", result.Messages)
	}
	if err := ApplyRefBindings(bindings, result.InputModel, params, nil); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	resolved, ok := params["metadataDb"].(map[string]interface{})
	if !ok || resolved["host"] != "corporate" {
		t.Fatalf("in-place substitution should carry the ClusterConnection values: %#v", params["metadataDb"])
	}
	if result.EffectiveInputConnections[0].Kind != kv1alpha1.KindClusterConnection {
		t.Errorf("unexpected effective kind: %+v", result.EffectiveInputConnections[0])
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

// A consumer waiting on an existing but broken connection must surface the
// root cause (phase, producer release, message), not a bare 'waiting'.
func TestWaitingMessageCarriesRootCause(t *testing.T) {
	c := readyConnection("kcd-trino-endpoint", "okdp", "trino", `{}`)
	c.Status.Phase = kv1alpha1.ConnectionPhaseError
	c.Status.Parent = "trino"
	c.Status.Message = "helm install failed"
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(c).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("trino", "parameters.trino", "kcd-trino-endpoint", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected one waiting message, got %v", result.Messages)
	}
	m := result.Messages[0]
	for _, want := range []string{"ERROR", "producer release trino", "helm install failed"} {
		if !strings.Contains(m, want) {
			t.Fatalf("message must carry %q, got: %s", want, m)
		}
	}
}

// A candidate that is NOT ready must already be watched, otherwise its
// transition to READY would only be seen at the next periodic requeue.
func TestNotReadyCandidateIsWatched(t *testing.T) {
	c := readyConnection("pending-db", "okdp", "database-server", `{"host":"pg"}`)
	c.Status.Phase = kv1alpha1.ConnectionPhaseError
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(c).Build()
	helper := &bimTestHelper{Client: cl}

	inputs := []kubopackage.InputRendered{namedInput("database-server", "db", "pending-db", "okdp", "")}
	result, rerr := BuildInputModel(context.Background(), helper, inputs)
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	var watched bool
	for _, w := range result.WatchedInputConnections {
		if w.Name == "pending-db" && w.Kind == kv1alpha1.KindConnection {
			watched = true
		}
	}
	if !watched {
		t.Fatalf("a non-ready candidate must be watched, got %+v", result.WatchedInputConnections)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected the release to be gated, got %v", result.Messages)
	}
}

// The data model exposed to the templates: a hand-written stanza input keeps
// feeding .Inputs / .InputLists, while a generated ref input is substituted in
// place and removed from both.
func TestDataModelSplitsStanzaAndGeneratedRefs(t *testing.T) {
	stanzaCnx := readyConnection("shared-s3", "okdp", "s3", `{"endpoint":"s3.okdp"}`)
	refCnx := readyConnection("kcd-pg-app", "okdp", "database-server", `{"host":"pg.okdp"}`)
	cl := fake.NewClientBuilder().WithScheme(bimScheme(t)).WithObjects(stanzaCnx, refCnx).Build()
	helper := &bimTestHelper{Client: cl}

	stanza := namedInput("s3", "store", "shared-s3", "okdp", "")
	stanza.AllowMultiple = true
	parameters := map[string]interface{}{"db": "kcd-pg-app"}
	generated, bindings, err := GenerateRefInputs(
		[]kuboschema.ConnectionDecl{{Path: []string{"db"}, Interface: "database-server", Required: true}},
		nil, parameters, nil, "okdp", []kubopackage.InputRendered{stanza})
	if err != nil {
		t.Fatalf("GenerateRefInputs failed: %v", err)
	}
	result, rerr := BuildInputModel(context.Background(), helper, append([]kubopackage.InputRendered{stanza}, generated...))
	if rerr != nil {
		t.Fatalf("BuildInputModel failed: %v", rerr)
	}
	if err := ApplyRefBindings(bindings, result.InputModel, parameters, nil); err != nil {
		t.Fatalf("ApplyRefBindings failed: %v", err)
	}
	model := BuildModel(nil, parameters, &kv1alpha1.Release{}, nil)
	model["Inputs"] = result.InputModel
	model["InputLists"] = result.InputListModel

	// The stanza is still served by both spaces
	if _, ok := model["Inputs"].(map[string]interface{})["store"]; !ok {
		t.Fatalf(".Inputs must expose the stanza alias, got %#v", model["Inputs"])
	}
	if _, ok := model["InputLists"].(map[string]interface{})["store"]; !ok {
		t.Fatalf(".InputLists must expose the stanza alias, got %#v", model["InputLists"])
	}
	// The generated ref is substituted in place and absent from both
	values, ok := parameters["db"].(map[string]interface{})
	if !ok || values["host"] != "pg.okdp" {
		t.Fatalf("the ref must be substituted in place, got %#v", parameters["db"])
	}
	for _, space := range []string{"Inputs", "InputLists"} {
		if _, leaked := model[space].(map[string]interface{})["parameters.db"]; leaked {
			t.Fatalf("the generated input must not leak into .%s", space)
		}
	}
}
