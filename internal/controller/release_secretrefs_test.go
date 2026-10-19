package controller

import (
	"context"
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/kuboschema"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	secretRefTestNamespace = "okdp"
	secretRefTestContract  = "database-server"
	secretRefTestAlias     = "parameters.db"
	secretRefTestField     = "credentials"
)

func newSecretRefTestClient(t *testing.T, objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func testSecret(name string, keys ...string) *corev1.Secret {
	data := make(map[string][]byte, len(keys))
	for _, k := range keys {
		data[k] = []byte("x")
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: secretRefTestNamespace}, Data: data}
}

var dbSecretDecl = kuboschema.SecretRefDecl{Path: []string{"db"}, Keys: []string{"username", "password"}, Required: true}

func TestCheckSecretRefs(t *testing.T) {
	cases := []struct {
		name    string
		secrets []client.Object
		want    string
	}{
		{"present", []client.Object{testSecret("pg", "username", "password", "uri")}, ""},
		{"missing", nil, "parameters.db: Secret 'pg' not found in namespace 'okdp'"},
		{"other namespace", []client.Object{&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pg", Namespace: "other"}}}, "not found in namespace 'okdp'"},
		{"missing key", []client.Object{testSecret("pg", "username")}, "parameters.db: Secret 'pg' in namespace 'okdp' is missing keys: password"},
	}
	for _, c := range cases {
		messages, rErr := CheckSecretRefs(context.Background(), newSecretRefTestClient(t, c.secrets...), secretRefTestNamespace,
			[]kuboschema.SecretRefDecl{dbSecretDecl}, map[string]interface{}{"db": "pg"}, "parameters")
		if rErr != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, rErr)
		}
		if c.want == "" {
			if len(messages) != 0 {
				t.Errorf("%s: expected no message, got %v", c.name, messages)
			}
			continue
		}
		if len(messages) != 1 || !strings.Contains(messages[0], c.want) {
			t.Errorf("%s: expected '%s', got %v", c.name, c.want, messages)
		}
	}
}

func TestCheckSecretRefsInArray(t *testing.T) {
	decl := kuboschema.SecretRefDecl{Path: []string{"buckets", "[]", secretRefTestField}, Keys: []string{"accessKey", "secretKey"}}
	tree := map[string]interface{}{
		"buckets": []interface{}{
			map[string]interface{}{secretRefTestField: "s3-a"},
			map[string]interface{}{secretRefTestField: "s3-b"},
			map[string]interface{}{},
		},
	}
	cl := newSecretRefTestClient(t, testSecret("s3-a", "accessKey", "secretKey"))
	messages, rErr := CheckSecretRefs(context.Background(), cl, secretRefTestNamespace,
		[]kuboschema.SecretRefDecl{decl}, tree, "parameters")
	if rErr != nil {
		t.Fatal(rErr)
	}
	if len(messages) != 1 || !strings.HasPrefix(messages[0], "parameters.buckets[1].credentials: Secret 's3-b' not found") {
		t.Errorf("unexpected messages: %v", messages)
	}
}

func TestCheckSecretRefsEmptyName(t *testing.T) {
	cl := newSecretRefTestClient(t)
	optional := kuboschema.SecretRefDecl{Path: []string{"db"}, Keys: []string{"username"}}
	if messages, rErr := CheckSecretRefs(context.Background(), cl, secretRefTestNamespace, []kuboschema.SecretRefDecl{optional}, map[string]interface{}{"db": ""}, "parameters"); rErr != nil || len(messages) != 0 {
		t.Errorf("an empty optional name must be skipped, got %v / %v", messages, rErr)
	}
	_, rErr := CheckSecretRefs(context.Background(), cl, secretRefTestNamespace, []kuboschema.SecretRefDecl{dbSecretDecl}, map[string]interface{}{"db": ""}, "parameters")
	if rErr == nil || !rErr.IsFatal() || !strings.Contains(rErr.Error(), "no Secret name provided") {
		t.Errorf("an empty required name must be a fatal error, got %v", rErr)
	}
}

func TestCheckBoundSecretRefs(t *testing.T) {
	contract := &kv1alpha1.ClusterContract{
		ObjectMeta: metav1.ObjectMeta{Name: secretRefTestContract},
		Spec: kv1alpha1.ClusterContractSpec{
			Schema: &apiextensionsv1.JSON{Raw: []byte(`{"properties":{"host":{"type":"string"},"secretRef":{"type":"secretRef","keys":["username","password"],"required":true}}}`)},
		},
	}
	inputs := []*kubopackage.InputRendered{{Alias: "stanza"}, {Alias: secretRefTestAlias}}
	inputs[1].Contract = secretRefTestContract
	result := &BuildInputModelResult{
		InputModel: map[string]interface{}{
			"stanza":           map[string]interface{}{"secretRef": "ignored"},
			secretRefTestAlias: map[string]interface{}{"host": "pg", "secretRef": "pg-app"},
		},
		EffectiveInputConnections: []kv1alpha1.InputConnectionReference{
			{},
			{Kind: kv1alpha1.KindConnection, Name: "pg", Namespace: secretRefTestNamespace},
		},
	}
	bindings := []RefBinding{{Alias: secretRefTestAlias, Path: []string{"db"}}}
	cl := newSecretRefTestClient(t, contract, testSecret("pg-app", "username"))
	messages, rErr := CheckBoundSecretRefs(context.Background(), cl, cl, secretRefTestNamespace, inputs, bindings, result)
	if rErr != nil {
		t.Fatal(rErr)
	}
	if len(messages) != 1 || messages[0] != "parameters.db.secretRef: Secret 'pg-app' in namespace 'okdp' is missing keys: password" {
		t.Errorf("unexpected messages: %v", messages)
	}
}
