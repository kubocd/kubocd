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
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/configstore"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Release with a duration out of range", func() {
	It("is stored, does not stop the listing of the others, and fails alone with its text kept", func(sctx SpecContext) {
		const ns = "release-durations"
		Expect(k8sClient.Create(sctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		release := func(name string, pck map[string]interface{}, overrides ...interface{}) *unstructured.Unstructured {
			pck["repository"], pck["tag"] = "quay.io/kubocd/packages/demo", "0.1.0"
			spec := map[string]interface{}{"package": pck}
			if len(overrides) > 0 {
				spec["moduleOverrides"] = overrides
			}
			return &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": kv1alpha1.GroupVersion.String(), "kind": "Release",
				"metadata": map[string]interface{}{"name": name, "namespace": ns}, "spec": spec,
			}}
		}
		// The patterns of spec.package refuse a text that is not a duration, a negative one, and hours on timeout
		for name, pck := range map[string]map[string]interface{}{
			"garbage-interval": {"interval": "garbage"}, "negative-interval": {"interval": "-5m"}, "hour-timeout": {"timeout": "9999999h"},
		} {
			Expect(apierrors.IsInvalid(k8sClient.Create(sctx, release(name, pck)))).To(BeTrue(), name)
		}
		// They do not bound the number, and the overrides have no pattern: the API server accepts these
		invalid := map[string]struct {
			object *unstructured.Unstructured
			field  []string
			want   string
		}{
			"interval": {release("interval", map[string]interface{}{"interval": "9999999h"}), []string{"package", "interval"},
				`spec.package.interval: "9999999h" is not a duration of at most about 2562047h, such as 90s or 2m30s`},
			"timeout": {release("timeout", map[string]interface{}{"timeout": "9999999999m"}), []string{"package", "timeout"},
				`spec.package.timeout: "9999999999m" is not a duration`},
			"override-timeout": {release("override-timeout", map[string]interface{}{},
				map[string]interface{}{"module": "main", "timeout": "9999999h"}), nil,
				`spec.moduleOverrides[0].timeout: "9999999h" is not a duration`},
			"override-interval": {release("override-interval", map[string]interface{}{},
				map[string]interface{}{"module": "main", "interval": "garbage"}), nil,
				`spec.moduleOverrides[0].interval: "garbage" is not a duration`},
		}
		for _, c := range invalid {
			Expect(k8sClient.Create(sctx, c.object)).To(Succeed())
		}
		Expect(k8sClient.Create(sctx, release("sane", map[string]interface{}{"interval": "5m"}))).To(Succeed())

		releases := &kv1alpha1.ReleaseList{}
		Expect(k8sClient.List(sctx, releases, client.InNamespace(ns))).To(Succeed())
		Expect(releases.Items).To(HaveLen(5))

		informers, err := cache.New(cfg, cache.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(informers.IndexField(sctx, &kv1alpha1.ClusterConnection{}, ReleaseIndexOnOutputClusterConnection, func(o client.Object) []string {
			return OutputClusterConnectionReleaseKeys(o.(*kv1alpha1.ClusterConnection))
		})).To(Succeed())
		informerCtx, stop := context.WithCancel(sctx)
		defer stop()
		go func() { _ = informers.Start(informerCtx) }()
		// A Release the informer cannot decode would keep the cache from ever syncing
		cached := &kv1alpha1.ReleaseList{}
		Eventually(func() error {
			listCtx, cancelList := context.WithTimeout(sctx, 5*time.Second)
			defer cancelList()
			return informers.List(listCtx, cached, client.InNamespace(ns))
		}).WithTimeout(20 * time.Second).Should(Succeed())
		Expect(cached.Items).To(HaveLen(5))

		store := configstore.New()
		Expect(store.AddConfigs(&kv1alpha1.ConfigList{}, "kubocd")).To(Succeed())
		reconciler := &ReleaseReconciler{Client: k8sClient, EventRecorder: record.NewFakeRecorder(100), Logger: logr.Discard(),
			ServerRoot: GinkgoT().TempDir(), ConfigStore: store, RoleStore: rolestore.New(store, logr.Discard())}
		for name, c := range invalid {
			key := types.NamespacedName{Namespace: ns, Name: name}
			for range 2 { // The first reconciliation adds the finalizer
				result, err := reconciler.Reconcile(sctx, ctrl.Request{NamespacedName: key})
				Expect(err).NotTo(HaveOccurred(), name)
				Expect(result.RequeueAfter).To(BeZero(), name)
			}
			stored := &unstructured.Unstructured{}
			stored.SetGroupVersionKind(kv1alpha1.GroupVersion.WithKind("Release"))
			Expect(k8sClient.Get(sctx, key, stored)).To(Succeed())
			Expect(stored.GetFinalizers()).To(ContainElement(global.FinalizerName), name)
			field := c.field
			if field == nil {
				field = []string{"moduleOverrides"}
			}
			written, _, _ := unstructured.NestedFieldNoCopy(c.object.Object, append([]string{"spec"}, field...)...)
			kept, _, _ := unstructured.NestedFieldNoCopy(stored.Object, append([]string{"spec"}, field...)...)
			Expect(kept).To(Equal(written), "%s: the text of the user is not kept", name)
			phase, _, _ := unstructured.NestedString(stored.Object, "status", "phase")
			Expect(phase).To(Equal(string(kv1alpha1.ReleasePhaseError)), name)
			observed, _, _ := unstructured.NestedInt64(stored.Object, "status", "observedGeneration")
			Expect(observed).To(Equal(stored.GetGeneration()), name)
			conditions, _, _ := unstructured.NestedSlice(stored.Object, "status", "conditions")
			Expect(conditions).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("type", "Ready"), HaveKeyWithValue("status", "False"), HaveKeyWithValue("reason", "Failed"),
				HaveKeyWithValue("message", HavePrefix(c.want)),
				HaveKeyWithValue("observedGeneration", BeNumerically("==", stored.GetGeneration())))), name)
		}

		// The sane Release is reconciled from the same API server: it gets its finalizer
		sane := types.NamespacedName{Namespace: ns, Name: "sane"}
		_, _ = reconciler.Reconcile(sctx, ctrl.Request{NamespacedName: sane})
		saneRelease := &kv1alpha1.Release{}
		Expect(k8sClient.Get(sctx, sane, saneRelease)).To(Succeed())
		Expect(saneRelease.Finalizers).To(ContainElement(global.FinalizerName))

		// An invalid Release can be deleted, by a reconciler that reads from the informers
		reconciler.Client, err = client.New(cfg, client.Options{Scheme: scheme.Scheme, Cache: &client.CacheOptions{Reader: informers}})
		Expect(err).NotTo(HaveOccurred())
		key := types.NamespacedName{Namespace: ns, Name: "interval"}
		Expect(k8sClient.Delete(sctx, invalid["interval"].object)).To(Succeed())
		Eventually(func() bool {
			_, _ = reconciler.Reconcile(sctx, ctrl.Request{NamespacedName: key})
			return apierrors.IsNotFound(k8sClient.Get(sctx, key, &kv1alpha1.Release{}))
		}).WithTimeout(20 * time.Second).Should(BeTrue())
	})
})
