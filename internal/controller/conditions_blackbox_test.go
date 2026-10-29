//nolint:goconst // Keep policy inputs and expected results together.
package controller

import (
	"context"
	"errors"
	"fmt"
	kv1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const closedPolicyMessage = "only core Secret and ConfigMap conditions in the Release target namespace are allowed"
const conditionReadDenied = "condition read forbidden: grant get access to the condition kind in the Release target namespace"

type conditionReadClient struct {
	client.Client
	reads, mappings int
	key             client.ObjectKey
	gvk             schema.GroupVersionKind
	unstructured    bool
	readError       error
}

func (c *conditionReadClient) RESTMapper() meta.RESTMapper {
	c.mappings++
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	for _, kind := range []string{"Secret", "ConfigMap", "Lease", "Connection", "Namespace"} {
		mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: kind}, meta.RESTScopeNamespace)
	}
	return mapper
}

func (c *conditionReadClient) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	c.reads++
	c.key = key
	c.gvk = obj.GetObjectKind().GroupVersionKind()
	u, ok := obj.(*unstructured.Unstructured)
	c.unstructured = ok
	if c.readError != nil {
		return c.readError
	}
	if !ok {
		return errors.New("condition read must be unstructured")
	}
	u.Object["data"] = map[string]interface{}{"password": "object-canary"}
	return nil
}

func TestCheckConditionClosedKinds(t *testing.T) {
	for _, tc := range []struct{ group, kind string }{
		{"", "Lease"}, {"coordination.k8s.io", "Lease"}, {"kubocd.kubotal.io", "Connection"},
		{"example.invalid", "Widget"}, {"", "Namespace"}, {"", "Node"},
		{"apiextensions.k8s.io", "CustomResourceDefinition"}, {"not-core", "Secret"},
		{"v1", "ConfigMap"}, {"", "CONFİGMAP"}, {"", "secrets"}, {"", " Secret"},
	} {
		t.Run(tc.group+"/"+tc.kind, func(t *testing.T) {
			c := &conditionReadClient{}
			ok, msg, err := CheckCondition(context.Background(), c, &kubopackage.ConditionRendered{
				Group: tc.group, Kind: kv1.Kind(tc.kind), Namespace: "target", Name: "name-canary", Cel: "'expression-canary' == 'expression-canary'",
			})
			if ok || msg != "" || err == nil || !err.IsFatal() || err.GetEventReason() != "ConditionError" || err.Error() != closedPolicyMessage {
				t.Errorf("got ok=%t message=%q error=%v, want fatal fixed policy refusal", ok, msg, err)
			}
			if c.reads != 0 || c.mappings != 0 {
				t.Errorf("forbidden condition accessed client: reads=%d mappings=%d", c.reads, c.mappings)
			}
		})
	}
	for _, kind := range []string{"secret", "Secret", "SECRET", "configmap", "ConfigMap", "CONFIGMAP"} {
		t.Run(kind, func(t *testing.T) {
			for _, expr := range []string{"", "'password' in data", "false"} {
				c := &conditionReadClient{}
				ok, msg, err := CheckCondition(context.Background(), c, &kubopackage.ConditionRendered{Kind: kv1.Kind(kind), Namespace: "target", Name: "db", Cel: expr})
				if err != nil || ok != (expr != "false") || (expr == "false" && !strings.Contains(msg, "the cel expression is false")) {
					t.Errorf("expression %q: ok=%t message=%q error=%v", expr, ok, msg, err)
				}
				canonical := "Secret"
				if strings.EqualFold(kind, "configmap") {
					canonical = "ConfigMap"
				}
				if c.reads != 1 || c.mappings != 0 || !c.unstructured || c.key != (client.ObjectKey{Namespace: "target", Name: "db"}) ||
					c.gvk != (schema.GroupVersionKind{Version: "v1", Kind: canonical}) {
					t.Errorf("wrong read: %+v", c)
				}
			}
		})
	}
}

func TestConditionReadPermissionError(t *testing.T) {
	resource := schema.GroupResource{Resource: "secrets"}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"denied", apierrors.NewForbidden(resource, "name-canary", errors.New("object-canary")), conditionReadDenied},
		{"not found", apierrors.NewNotFound(resource, "db"), ""},
		{"other API error", apierrors.NewServiceUnavailable("object-canary"),
			"unable to fetch the object: the API server answered ServiceUnavailable, message withheld, as it can quote the object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &conditionReadClient{readError: tc.err}
			ok, msg, err := CheckCondition(context.Background(), c, &kubopackage.ConditionRendered{Kind: "Secret", Namespace: "target", Name: "db"})
			if ok {
				t.Fatal("failed read passed condition")
			}
			if tc.want == "" {
				if err != nil || !strings.Contains(msg, "object does not exists") {
					t.Fatalf("missing object: %q %v", msg, err)
				}
			} else if msg != "" || err == nil || err.IsFatal() || err.GetEventReason() != "ConditionError" || err.Error() != tc.want {
				t.Fatalf("got message=%q error=%v, want nonfatal %q", msg, err, tc.want)
			}
		})
	}
}

func TestReleaseConditionPolicyIsDefinitive(t *testing.T) {
	for _, fields := range []string{"group: coordination.k8s.io\n    kind: Lease", "kind: Secret\n    namespace: foreign-canary"} {
		for _, preceding := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/preceding=%t", fields, preceding), func(t *testing.T) {
				prefix := "conditions: |\n"
				index := 0
				if preceding {
					prefix += "  - kind: Secret\n    name: absent\n    cel: 'false'\n"
					index = 1
				}
				manifest := fmt.Sprintf(afterRenderPackage, prefix+"  - "+fields+"\n    name: name-canary\n    cel: 'expression-canary'", "")
				bed := newReleaseTestbed(t, manifest, "", "")
				release := &kv1.Release{}
				key := client.ObjectKey{Namespace: "apps", Name: "demo"}
				if err := bed.client.Get(context.Background(), key, release); err != nil {
					t.Fatal(err)
				}
				release.Spec.TargetNamespace = "target"
				if err := bed.client.Update(context.Background(), release); err != nil {
					t.Fatal(err)
				}
				got := bed.reconcile(t)
				want := fmt.Sprintf("error on rendering: condition[%d]: %s", index, closedPolicyMessage)
				if got.status != want || got.err != nil || !got.result.IsZero() {
					t.Fatalf("got %+v, want definitive %q", got, want)
				}
				if err := bed.client.Get(context.Background(), key, release); err != nil {
					t.Fatal(err)
				}
				if release.Status.Phase != kv1.ReleasePhaseError {
					t.Errorf("phase=%s", release.Status.Phase)
				}
				if !strings.Contains(strings.Join(got.events, "\n"), "Warning ConditionError "+want) {
					t.Errorf("events=%v", got.events)
				}
				for sink, value := range got.sinks() {
					if strings.Contains(value, "canary") {
						t.Errorf("%s leaked condition: %s", sink, value)
					}
				}
			})
		}
	}
	for _, forbidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("ordinary condition forbidden=%t", forbidden), func(t *testing.T) {
			manifest := fmt.Sprintf(afterRenderPackage, "conditions: |\n  - kind: ConfigMap\n    name: db\n    cel: 'false'", "")
			bed := newReleaseTestbed(t, manifest, "", "", func(r *ReleaseReconciler) {
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if u, ok := obj.(*unstructured.Unstructured); ok {
						if forbidden {
							return apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, key.Name, errors.New("object-canary"))
						}
						u.Object["data"] = map[string]interface{}{"password": "value"}
						return nil
					}
					return c.Get(ctx, key, obj, opts...)
				}})
			})
			got := bed.reconcile(t)
			if forbidden {
				if got.err == nil || got.status != "condition[0]: "+conditionReadDenied {
					t.Fatalf("forbidden must report retryable error: %+v", got)
				}
			} else if got.err != nil || got.result.RequeueAfter != 5*time.Second || !strings.Contains(got.status, "the cel expression is false") {
				t.Fatalf("ordinary false must wait 5s: %+v", got)
			}
		})
	}
}
