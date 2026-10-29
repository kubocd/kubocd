//nolint:goconst // Expected authorization tuples remain explicit.
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

func TestGeneratedControllerRBACMatchesChart(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	generated := map[string]string{}
	for _, doc := range releaseutil.SplitManifests(string(raw)) {
		var header struct {
			Kind     string            `json:"kind"`
			Metadata metav1.ObjectMeta `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &header); err != nil {
			t.Fatal(err)
		}
		key := header.Kind + "/" + header.Metadata.Name
		if _, exists := generated[key]; exists {
			t.Fatalf("duplicate generated object %s", key)
		}
		generated[key] = doc
	}
	assertRBACObjects(t, generated, []string{"ClusterRole/manager-role", "Role/leader-election-role"})
	chart := renderChart(t, "kubocd-ctrl", func(v map[string]interface{}) {
		gates := v["controller"].(map[string]interface{})["featureGates"].(map[string]interface{})
		gates["connections"], gates["replications"] = true, true
	})
	generatedMain, chartMain := &rbacv1.ClusterRole{}, &rbacv1.ClusterRole{}
	decodeStrict(t, generated, "ClusterRole", "manager-role", generatedMain)
	decodeStrict(t, chart, "ClusterRole", "kubocd-ctrl-controller", chartMain)
	assertRules(t, generatedMain.Rules, chartMain.Rules)
	assertRules(t, generatedMain.Rules, controllerRules(true))
	generatedLease, chartLease := &rbacv1.Role{}, &rbacv1.Role{}
	decodeStrict(t, generated, "Role", "leader-election-role", generatedLease)
	decodeStrict(t, chart, "Role", "kubocd-ctrl-controller-leader-election", chartLease)
	assertRules(t, generatedLease.Rules, chartLease.Rules)
	if generatedMain.Namespace != "" || generatedLease.Namespace != "kubocd" {
		t.Error("incorrect generated RBAC scopes")
	}
}

type authorizationExpectation struct {
	attributes authorizationv1.ResourceAttributes
	allowed    bool
}

func controllerAuthorizationExpectations(replications bool) []authorizationExpectation {
	allVerbs := strings.Split("get,list,watch,create,update,patch,delete,deletecollection,bind,escalate,impersonate", ",")
	expected := controllerRules(replications)
	// Include every removed generated subresource, even ones never granted by the chart.
	inventory := append(controllerRules(true), rbacRule("kubocd.kubotal.io",
		"configs/status,configs/finalizers,contexts/finalizers,contracts/finalizers,clustercontracts/finalizers,"+
			"connections/finalizers,clusterconnections/finalizers,replications/finalizers", "update"))
	var result []authorizationExpectation
	for _, rule := range inventory {
		for _, resource := range rule.Resources {
			for _, verb := range allVerbs {
				allowed := false
				for _, want := range expected {
					if slices.Contains(want.APIGroups, rule.APIGroups[0]) && slices.Contains(want.Resources, resource) &&
						slices.Contains(want.Verbs, verb) {
						allowed = true
					}
				}
				name, subresource, _ := strings.Cut(resource, "/")
				ns := "rbac-foreign"
				if strings.HasPrefix(name, "cluster") {
					ns = ""
				}
				result = append(result, authorizationExpectation{attributes: authorizationv1.ResourceAttributes{
					Group: rule.APIGroups[0], Resource: name, Subresource: subresource, Verb: verb, Namespace: ns,
				}, allowed: allowed})
			}
		}
	}
	for _, ns := range []string{"rbac-install", "rbac-foreign", ""} {
		for _, verb := range allVerbs {
			result = append(result, authorizationExpectation{attributes: authorizationv1.ResourceAttributes{
				Group: "coordination.k8s.io", Resource: "leases", Verb: verb, Namespace: ns,
			}, allowed: ns == "rbac-install" && slices.Contains([]string{"get", "create", "update"}, verb)})
		}
	}
	return result
}

func installControllerRBAC(t *testing.T, api *apiServer, docs map[string]string) {
	t.Helper()
	for _, kind := range []string{"ServiceAccount", "ClusterRole", "Role", "ClusterRoleBinding", "RoleBinding"} {
		for key := range docs {
			docKind, name, _ := strings.Cut(key, "/")
			if kind != docKind {
				continue
			}
			var obj client.Object
			switch kind {
			case "ServiceAccount":
				obj = &corev1.ServiceAccount{}
			case "ClusterRole":
				obj = &rbacv1.ClusterRole{}
			case "Role":
				obj = &rbacv1.Role{}
			case "ClusterRoleBinding":
				obj = &rbacv1.ClusterRoleBinding{}
			case "RoleBinding":
				obj = &rbacv1.RoleBinding{}
			}
			decodeStrict(t, docs, kind, name, obj)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := api.client.Create(ctx, obj)
			cancel()
			if err != nil {
				t.Fatalf("create %s: %v", key, err)
			}
		}
	}
}

func TestControllerChartAuthorization(t *testing.T) {
	api := startAPIServer(t, []string{"rbac-install", "rbac-foreign"})
	cfg := rest.CopyConfig(api.cfg)
	cfg.QPS, cfg.Burst = 200, 400
	reviewer, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, connections := range []bool{false, true} {
		for _, replications := range []bool{false, true} {
			t.Run(fmt.Sprintf("connections=%t/replications=%t", connections, replications), func(t *testing.T) {
				release := fmt.Sprintf("auth-%t-%t", connections, replications)
				docs := renderRelease(t, "kubocd-ctrl", release, "rbac-install", func(v map[string]interface{}) {
					gates := v["controller"].(map[string]interface{})["featureGates"].(map[string]interface{})
					gates["connections"], gates["replications"] = connections, replications
				})
				installControllerRBAC(t, api, docs)
				subject := "system:serviceaccount:rbac-install:" + release + "-controller"
				check := func(ctx context.Context, expectation authorizationExpectation) error {
					review, err := reviewer.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
						Spec: authorizationv1.SubjectAccessReviewSpec{User: subject,
							Groups: []string{"system:serviceaccounts",
								"system:serviceaccounts:rbac-install", "system:authenticated"},
							ResourceAttributes: &expectation.attributes},
					}, metav1.CreateOptions{})
					if err != nil {
						return err
					}
					if review.Status.EvaluationError != "" {
						return fmt.Errorf("SAR evaluation: %s", review.Status.EvaluationError)
					}
					if review.Status.Allowed != expectation.allowed {
						return fmt.Errorf("%+v allowed=%t, want %t: %s", expectation.attributes,
							review.Status.Allowed, expectation.allowed, review.Status.Reason)
					}
					return nil
				}
				expectations := controllerAuthorizationExpectations(replications)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				var last error
				err := wait.PollUntilContextCancel(ctx, 50*time.Millisecond, true, func(ctx context.Context) (bool, error) {
					for _, expectation := range expectations {
						if !expectation.allowed {
							continue
						}
						if last = check(ctx, expectation); last != nil {
							return false, nil
						}
					}
					return true, nil
				})
				if err != nil {
					t.Fatalf("RBAC did not propagate within 30s: %v; last: %v", err, last)
				}
				for _, expectation := range expectations {
					if expectation.allowed {
						continue
					}
					if err := check(ctx, expectation); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
