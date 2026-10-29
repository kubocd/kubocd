//nolint:goconst // Keep the complete expected permission table beside its assertions.
package cmd

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

func rbacRule(group, resources, verbs string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: strings.Split(resources, ","),
		Verbs: strings.Split(verbs, ",")}
}

func controllerRules(replications bool) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{
		rbacRule("kubocd.kubotal.io", "releases", "get,list,watch,update"),
		rbacRule("kubocd.kubotal.io", "releases/finalizers", "update"),
		rbacRule("kubocd.kubotal.io", "releases/status,contexts/status", "update"),
		rbacRule("kubocd.kubotal.io", "contexts,contracts,clustercontracts,configs", "get,list,watch"),
		rbacRule("kubocd.kubotal.io", "connections,clusterconnections", "get,list,watch,create,patch,delete"),
		rbacRule("kubocd.kubotal.io",
			"connections/status,clusterconnections/status,contracts/status,clustercontracts/status", "patch"),
		rbacRule("source.toolkit.fluxcd.io", "ocirepositories", "get,list,watch,create,patch"),
		rbacRule("source.toolkit.fluxcd.io", "helmrepositories", "get,list,watch,create,patch,delete"),
		rbacRule("helm.toolkit.fluxcd.io", "helmreleases", "get,list,watch,create,patch,delete"),
		rbacRule("", "events", "create,patch"),
	}
	if replications {
		rules = append(rules,
			rbacRule("kubocd.kubotal.io", "replications", "get,list,watch,update"),
			rbacRule("kubocd.kubotal.io", "replications/status", "patch"),
			rbacRule("", "secrets,configmaps", "get,list,watch,create,update,delete"))
	}
	return rules
}

func electionRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{rbacRule("coordination.k8s.io", "leases", "get,create,update")}
}

func permissionTuples(t *testing.T, rules []rbacv1.PolicyRule) []string {
	t.Helper()
	tuples := map[string]bool{}
	add := func(tuple string) {
		if tuples[tuple] {
			t.Errorf("duplicate permission: %s", tuple)
		}
		tuples[tuple] = true
	}
	for i, rule := range rules {
		for field, values := range map[string][]string{
			"groups": rule.APIGroups, "resources": rule.Resources, "verbs": rule.Verbs, "URLs": rule.NonResourceURLs,
		} {
			sorted := slices.Sorted(slices.Values(values))
			if len(slices.Compact(sorted)) != len(values) {
				t.Errorf("rule %d has duplicate %s: %v", i, field, values)
			}
		}
		if len(rule.ResourceNames) != 0 {
			t.Errorf("rule %d unexpectedly restricts resource names: %v", i, rule.ResourceNames)
		}
		if len(rule.Verbs) == 0 || (len(rule.Resources) == 0 && len(rule.NonResourceURLs) == 0) {
			t.Errorf("empty rule: %+v", rule)
		}
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					add(group + "/" + resource + ":" + verb)
				}
			}
		}
		for _, url := range rule.NonResourceURLs {
			for _, verb := range rule.Verbs {
				add("URL:" + url + ":" + verb)
			}
		}
	}
	return slices.Sorted(maps.Keys(tuples))
}

func assertRules(t *testing.T, got, want []rbacv1.PolicyRule) {
	t.Helper()
	actual, expected := permissionTuples(t, got), permissionTuples(t, want)
	if !slices.Equal(actual, expected) {
		t.Errorf("permissions differ:\ngot  %v\nwant %v", actual, expected)
	}
}

func assertBinding(t *testing.T, ref rbacv1.RoleRef, subjects []rbacv1.Subject, kind, role, sa, ns string) {
	t.Helper()
	wantRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kind, Name: role}
	wantSubjects := []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: ns}}
	if ref != wantRef || !reflect.DeepEqual(subjects, wantSubjects) {
		t.Errorf("binding got %+v %+v, want %+v %+v", ref, subjects, wantRef, wantSubjects)
	}
}

func assertRBACObjects(t *testing.T, documents map[string]string, names []string) {
	t.Helper()
	var actual []string
	for key := range documents {
		kind, _, _ := strings.Cut(key, "/")
		if slices.Contains([]string{"ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"}, kind) {
			actual = append(actual, key)
		}
	}
	slices.Sort(actual)
	expected := slices.Sorted(slices.Values(names))
	if !slices.Equal(actual, expected) {
		t.Errorf("RBAC objects: got %v, want %v", actual, expected)
	}
}

func TestControllerChartExactRBAC(t *testing.T) {
	for _, names := range []struct{ release, ns, base, role, sa string }{
		{"kubocd-ctrl", "kubocd", "", "kubocd-ctrl-controller", "kubocd-ctrl-controller"},
		{"other", "installation", "custom-base", "custom-role", "custom-account"},
		{"other", "installation", "custom-base", "custom-base", "custom-base"},
	} {
		for _, connections := range []bool{false, true} {
			for _, replications := range []bool{false, true} {
				for _, replicas := range []int{1, 2} {
					name := fmt.Sprintf("%s/%s/connections=%t/replications=%t/replicas=%d",
						names.ns, names.role, connections, replications, replicas)
					t.Run(name, func(t *testing.T) {
						docs := renderRelease(t, "kubocd-ctrl", names.release, names.ns, func(v map[string]interface{}) {
							c := v["controller"].(map[string]interface{})
							c["replicaCount"], c["baseNameOverride"] = replicas, names.base
							if names.role != "custom-base" && names.base != "" {
								c["clusterRoleName"], c["serviceAccountName"] = names.role, names.sa
							}
							c["commonAnnotations"] = map[string]interface{}{"test.example/annotation": "retained"}
							gates := c["featureGates"].(map[string]interface{})
							gates["connections"], gates["replications"] = connections, replications
						})
						main := &rbacv1.ClusterRole{}
						decodeStrict(t, docs, "ClusterRole", names.role, main)
						assertRules(t, main.Rules, controllerRules(replications))
						if main.Namespace != "" {
							t.Errorf("ClusterRole has namespace %q", main.Namespace)
						}
						election := names.role + "-leader-election"
						role := &rbacv1.Role{}
						decodeStrict(t, docs, "Role", election, role)
						assertRules(t, role.Rules, electionRules())
						if role.Namespace != names.ns {
							t.Errorf("Lease Role namespace=%q, want %q", role.Namespace, names.ns)
						}
						binding := &rbacv1.RoleBinding{}
						decodeStrict(t, docs, "RoleBinding", election, binding)
						if binding.Namespace != names.ns {
							t.Errorf("Lease binding namespace=%q, want %q", binding.Namespace, names.ns)
						}
						assertBinding(t, binding.RoleRef, binding.Subjects, "Role", election, names.sa, names.ns)
						for _, obj := range []struct{ labels, annotations map[string]string }{
							{role.Labels, role.Annotations}, {binding.Labels, binding.Annotations},
						} {
							if !maps.Equal(obj.labels, main.Labels) || !maps.Equal(obj.annotations, main.Annotations) {
								t.Errorf("election metadata not propagated: %+v", obj)
							}
						}
						clusterBinding := &rbacv1.ClusterRoleBinding{}
						bindingName := names.sa + "-" + names.role + "-binding"
						decodeStrict(t, docs, "ClusterRoleBinding", bindingName, clusterBinding)
						assertBinding(t, clusterBinding.RoleRef, clusterBinding.Subjects, "ClusterRole", names.role, names.sa, names.ns)
						sa := &corev1.ServiceAccount{}
						decodeStrict(t, docs, "ServiceAccount", names.sa, sa)
						if sa.Namespace != names.ns || deploymentOf(t, docs).Spec.Template.Spec.ServiceAccountName != names.sa {
							t.Error("deployment and RBAC ServiceAccounts differ")
						}
						assertRBACObjects(t, docs, []string{"ServiceAccount/" + names.sa, "ClusterRole/" + names.role,
							"ClusterRoleBinding/" + bindingName, "Role/" + election, "RoleBinding/" + election})
					})
				}
			}
		}
	}
	t.Run("disabled", func(t *testing.T) {
		docs := renderChart(t, "kubocd-ctrl", func(v map[string]interface{}) {
			v["controller"].(map[string]interface{})["enabled"] = false
		})
		assertRBACObjects(t, docs, nil)
	})
}

func TestControllerChartMetricsRBAC(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, secured := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%t/secured=%t", enabled, secured), func(t *testing.T) {
				docs := renderChart(t, "kubocd-ctrl", func(v map[string]interface{}) {
					m := v["controller"].(map[string]interface{})["metrics"].(map[string]interface{})
					m["enabled"], m["secured"] = enabled, secured
				})
				base := "kubocd-ctrl-controller"
				names := []string{"ServiceAccount/" + base, "ClusterRole/" + base,
					"ClusterRoleBinding/" + base + "-" + base + "-binding",
					"Role/" + base + "-leader-election", "RoleBinding/" + base + "-leader-election"}
				if enabled && secured {
					auth, reader := &rbacv1.ClusterRole{}, &rbacv1.ClusterRole{}
					decodeStrict(t, docs, "ClusterRole", base+"-metrics-auth", auth)
					decodeStrict(t, docs, "ClusterRole", base+"-metrics-reader", reader)
					assertRules(t, auth.Rules, []rbacv1.PolicyRule{rbacRule("authentication.k8s.io", "tokenreviews", "create"),
						rbacRule("authorization.k8s.io", "subjectaccessreviews", "create")})
					assertRules(t, reader.Rules, []rbacv1.PolicyRule{{NonResourceURLs: []string{"/metrics"}, Verbs: []string{"get"}}})
					b := &rbacv1.ClusterRoleBinding{}
					decodeStrict(t, docs, "ClusterRoleBinding", base+"-"+base+"-metrics-auth", b)
					assertBinding(t, b.RoleRef, b.Subjects, "ClusterRole", base+"-metrics-auth", base, "kubocd")
					names = append(names, "ClusterRole/"+base+"-metrics-auth", "ClusterRole/"+base+"-metrics-reader",
						"ClusterRoleBinding/"+base+"-"+base+"-metrics-auth")
				}
				main := &rbacv1.ClusterRole{}
				decodeStrict(t, docs, "ClusterRole", base, main)
				assertRules(t, main.Rules, controllerRules(false))
				assertRBACObjects(t, docs, names)
			})
		}
	}
}
