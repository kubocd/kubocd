//nolint:goconst // Keep API fixtures beside their assertions.
package controller

import (
	"context"
	"fmt"
	kv1 "kubocd/api/v1alpha1"
	"kubocd/internal/kubopackage"
	"kubocd/internal/tmpl"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

const conditionSpecField = "spec"

type conditionTransport struct {
	http.RoundTripper
	lists *atomic.Int32
}

func (t conditionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodGet && (strings.HasSuffix(req.URL.Path, "/secrets") || strings.HasSuffix(req.URL.Path, "/configmaps")) {
		t.lists.Add(1)
	}
	return t.RoundTripper.RoundTrip(req)
}

var _ = Describe("Closed package conditions", func() {
	It("uses the target namespace and rejects discoverable foreign kinds", func(ctx SpecContext) {
		for _, ns := range []string{"condition-control", "condition-target", "condition-foreign"} {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: ns},
				Data: map[string][]byte{"other": []byte("decoy")}})).To(Succeed())
		}
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "condition-target", Name: "db"}, secret)).To(Succeed())
		secret.Data["password"] = []byte("value")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "condition-target"},
			Data: map[string]string{"password": "value"}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "lock", Namespace: "condition-target"}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &kv1.Connection{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "condition-target"},
			Spec: kv1.ConnectionSpec{Contract: "database"}})).To(Succeed())

		render := func(conditions string) (*kubopackage.Rendered, error) {
			p := &kubopackage.Package{}
			Expect(yaml.Unmarshal([]byte(fmt.Sprintf(afterRenderPackage, "conditions: |\n"+conditions, "")), p)).To(Succeed())
			Expect(p.Groom(nil, tmpl.PackageFunctions(false))).To(Succeed())
			release := &kv1.Release{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "condition-control"},
				Spec: kv1.ReleaseSpec{TargetNamespace: "condition-target"}}
			return p.Render(map[string]interface{}{"Release": map[string]interface{}{conditionSpecField: map[string]interface{}{"targetNamespace": "condition-target", "createNamespace": false}}}, release)
		}
		for _, kind := range []string{"secret", "Secret", "configmap", "ConfigMap"} {
			rendered, err := render("  - kind: " + kind + "\n    name: db\n    cel: \"'password' in data\"")
			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.Conditions[0].Namespace).To(Equal("condition-target"))
			ok, message, reconcileErr := CheckCondition(ctx, k8sClient, rendered.Conditions[0])
			Expect(reconcileErr).NotTo(HaveOccurred())
			Expect(message).To(BeEmpty())
			Expect(ok).To(BeTrue())
		}
		for _, fields := range []string{
			"group: coordination.k8s.io\n    kind: Lease\n    name: lock",
			"group: kubocd.kubotal.io\n    kind: Connection\n    name: db",
			"group: apiextensions.k8s.io\n    kind: CustomResourceDefinition\n    name: releases.kubocd.kubotal.io",
			"kind: Secret\n    name: db\n    namespace: condition-foreign",
		} {
			_, err := render("  - " + fields)
			Expect(err).To(MatchError("condition[0]: " + closedPolicyMessage))
		}
		for _, cond := range []*kubopackage.ConditionRendered{
			{Kind: "Secret", Name: "db", Namespace: "condition-target", Cel: "false"},
			{Kind: "ConfigMap", Name: "missing", Namespace: "condition-target"},
		} {
			ok, message, reconcileErr := CheckCondition(ctx, k8sClient, cond)
			Expect(reconcileErr).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(message).NotTo(BeEmpty())
		}
	}, SpecTimeout(25*time.Second))

	It("reads with get-only permission without core informers", func(ctx SpecContext) {
		const ns = "condition-reader"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: ns},
			Data: map[string][]byte{"password": []byte("value")}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: ns},
			Data: map[string]string{"password": "value"}})).To(Succeed())
		limited := rest.CopyConfig(cfg)
		limited.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":reader"}
		var lists atomic.Int32
		limited.Wrap(func(rt http.RoundTripper) http.RoundTripper {
			return conditionTransport{RoundTripper: rt, lists: &lists}
		})
		mgr, err := ctrl.NewManager(limited, ctrl.Options{Scheme: scheme.Scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		Expect(err).NotTo(HaveOccurred())
		managerCtx, cancelManager := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { cancelManager(); Eventually(done, 3*time.Second).Should(Receive(BeNil())) })
		Eventually(mgr.Elected(), 3*time.Second).Should(BeClosed())
		condition := &kubopackage.ConditionRendered{Kind: "Secret", Namespace: ns, Name: "db", Cel: "'password' in data"}
		ok, message, reconcileErr := CheckCondition(ctx, mgr.GetClient(), condition)
		Expect(ok).To(BeFalse())
		Expect(message).To(BeEmpty())
		Expect(reconcileErr).To(HaveOccurred())
		Expect(reconcileErr.Error()).To(Equal(conditionReadDenied))
		Expect(reconcileErr.IsFatal()).To(BeFalse())
		Expect(k8sClient.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: ns}, Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"secrets", "configmaps"}, Verbs: []string{"get"}},
		}})).To(Succeed())
		Expect(k8sClient.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: ns},
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "reader"},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "reader", Namespace: ns}}})).To(Succeed())
		for _, kind := range []kv1.Kind{"secret", "configmap"} {
			condition.Kind = kind
			Eventually(func() bool {
				passed, _, readErr := CheckCondition(ctx, mgr.GetClient(), condition)
				return passed && readErr == nil
			}, 10*time.Second, 50*time.Millisecond).Should(BeTrue())
		}
		Expect(lists.Load()).To(BeZero())
	}, SpecTimeout(25*time.Second))
})
