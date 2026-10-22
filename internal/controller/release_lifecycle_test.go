/*
Copyright 2026 CGI France

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

package controller

import (
	kv1alpha1 "kubocd/api/v1alpha1"
	"kubocd/internal/global"
	"kubocd/internal/rolestore"
	"time"

	fluxv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Stands for the helm-controller finalizer of a HelmRelease whose uninstall does not complete
const blockingFinalizer = "test.kubocd.kubotal.io/uninstall"

var _ = Describe("Release lifecycle", func() {
	var namespace string
	var reconciler *ReleaseReconciler

	BeforeEach(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "lifecycle-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		reconciler = &ReleaseReconciler{
			Client:        k8sClient,
			EventRecorder: &record.FakeRecorder{},
			Logger:        logr.Discard(),
			RoleStore:     rolestore.New(nil, logr.Discard()),
			ServerRoot:    GinkgoT().TempDir(),
		}
	})

	createRelease := func(name string, annotations map[string]string) *kv1alpha1.Release {
		release := &kv1alpha1.Release{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: annotations,
				Finalizers:  []string{global.FinalizerName},
			},
			Spec: kv1alpha1.ReleaseSpec{Package: kv1alpha1.PackageSourceSpec{Repository: "registry/pkg", Tag: "1.0.0"}},
		}
		Expect(k8sClient.Create(ctx, release)).To(Succeed())
		return release
	}

	createHelmRelease := func(release *kv1alpha1.Release, module string, blocked bool, dependsOn ...string) {
		hr := &fluxv2.HelmRelease{
			ObjectMeta: metav1.ObjectMeta{Name: BuildHelmReleaseName(release.Name, module), Namespace: namespace},
			Spec: fluxv2.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: time.Minute},
				Chart: &fluxv2.HelmChartTemplate{Spec: fluxv2.HelmChartTemplateSpec{
					Chart:     module,
					SourceRef: fluxv2.CrossNamespaceObjectReference{Kind: "HelmRepository", Name: "kcd-" + release.Name},
				}},
			},
		}
		for _, dep := range dependsOn {
			hr.Spec.DependsOn = append(hr.Spec.DependsOn, fluxv2.DependencyReference{Name: BuildHelmReleaseName(release.Name, dep)})
		}
		if blocked {
			hr.Finalizers = []string{blockingFinalizer}
		}
		Expect(ctrl.SetControllerReference(release, hr, k8sClient.Scheme())).To(Succeed())
		Expect(k8sClient.Create(ctx, hr)).To(Succeed())
	}

	getHelmRelease := func(name string) *fluxv2.HelmRelease {
		hr := &fluxv2.HelmRelease{}
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, hr)
		if apierrors.IsNotFound(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		return hr
	}

	isDeleting := func(name string) bool {
		hr := getHelmRelease(name)
		return hr != nil && !hr.DeletionTimestamp.IsZero()
	}

	unblock := func(name string) {
		hr := getHelmRelease(name)
		controllerutil.RemoveFinalizer(hr, blockingFinalizer)
		Expect(k8sClient.Update(ctx, hr)).To(Succeed())
		Expect(getHelmRelease(name)).To(BeNil())
	}

	failUninstall := func(name string, message string) {
		hr := getHelmRelease(name)
		hr.Status.Conditions = []metav1.Condition{{
			Type: "Ready", Status: metav1.ConditionFalse, Reason: "UninstallFailed", Message: message, LastTransitionTime: metav1.Now(),
		}}
		Expect(k8sClient.Status().Update(ctx, hr)).To(Succeed())
	}

	reconcile := func(release *kv1alpha1.Release) *kv1alpha1.Release {
		key := client.ObjectKeyFromObject(release)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		current := &kv1alpha1.Release{}
		err = k8sClient.Get(ctx, key, current)
		if apierrors.IsNotFound(err) {
			return nil
		}
		Expect(err).NotTo(HaveOccurred())
		return current
	}

	It("keeps a deleted Release until its HelmReleases are gone, dependents first", func() {
		release := createRelease("app", nil)
		createHelmRelease(release, "main", true)
		createHelmRelease(release, "providers", true, "main")
		other := createRelease("other", nil)
		createHelmRelease(other, "main", false)

		Expect(k8sClient.Delete(ctx, release)).To(Succeed())

		current := reconcile(release)
		Expect(current).NotTo(BeNil())
		Expect(current.Status.Phase).To(Equal(kv1alpha1.ReleasePhaseDeleting))
		Expect(current.Status.Message).To(Equal("HelmRelease app-providers: uninstalling"))
		Expect(current.Status.HelmReleaseStates).To(HaveKey("main"))
		Expect(current.Status.HelmReleaseStates).To(HaveKey("providers"))
		Expect(isDeleting("app-providers")).To(BeTrue())
		Expect(isDeleting("app-main")).To(BeFalse())

		failUninstall("app-providers", "pre-delete hook failed")
		current = reconcile(release)
		Expect(current.Status.Message).To(Equal("HelmRelease app-providers: pre-delete hook failed"))
		Expect(isDeleting("app-main")).To(BeFalse())

		unblock("app-providers")
		current = reconcile(release)
		Expect(current).NotTo(BeNil())
		Expect(current.Status.Phase).To(Equal(kv1alpha1.ReleasePhaseDeleting))
		Expect(current.Status.Message).To(Equal("HelmRelease app-main: uninstalling"))
		Expect(isDeleting("app-main")).To(BeTrue())

		unblock("app-main")
		Expect(reconcile(release)).To(BeNil())

		Expect(getHelmRelease("other-main")).NotTo(BeNil())
		Expect(isDeleting("other-main")).To(BeFalse())
	})

	It("deletes a Release at once when asked not to wait", func() {
		release := createRelease("app", map[string]string{SkipDeletionWaitAnnotation: "true"})
		createHelmRelease(release, "main", true)

		Expect(k8sClient.Delete(ctx, release)).To(Succeed())

		Expect(reconcile(release)).To(BeNil())
		Expect(isDeleting("app-main")).To(BeFalse())
	})

	It("removes the HelmReleases of modules no longer deployed, dependents first", func() {
		release := createRelease("app", nil)
		createHelmRelease(release, "a", true)
		createHelmRelease(release, "b", true, "a")
		createHelmRelease(release, "c", false, "a")
		op := &releaseOperation{ctx: ctx, logger: logr.Discard(), release: release}
		wanted := map[string]bool{"app-c": true}

		message, rErr := reconciler.pruneHelmReleases(op, wanted)
		Expect(rErr).NotTo(HaveOccurred())
		Expect(message).To(Equal("HelmRelease app-b (being removed): uninstalling"))
		Expect(isDeleting("app-b")).To(BeTrue())
		Expect(isDeleting("app-a")).To(BeFalse())

		failUninstall("app-b", "pre-delete hook failed")
		message, rErr = reconciler.pruneHelmReleases(op, wanted)
		Expect(rErr).NotTo(HaveOccurred())
		Expect(message).To(Equal("HelmRelease app-b (being removed): pre-delete hook failed"))

		unblock("app-b")
		message, rErr = reconciler.pruneHelmReleases(op, wanted)
		Expect(rErr).NotTo(HaveOccurred())
		Expect(message).To(Equal("HelmRelease app-a (being removed): uninstalling"))
		Expect(isDeleting("app-a")).To(BeTrue())

		unblock("app-a")
		message, rErr = reconciler.pruneHelmReleases(op, wanted)
		Expect(rErr).NotTo(HaveOccurred())
		Expect(message).To(BeEmpty())
		Expect(isDeleting("app-c")).To(BeFalse())
	})

	It("does not block on a dependency cycle", func() {
		release := createRelease("app", nil)
		createHelmRelease(release, "a", false, "b")
		createHelmRelease(release, "b", false, "a")
		op := &releaseOperation{ctx: ctx, logger: logr.Discard(), release: release}

		_, rErr := reconciler.pruneHelmReleases(op, map[string]bool{})
		Expect(rErr).NotTo(HaveOccurred())
		Expect(getHelmRelease("app-a")).To(BeNil())
		Expect(getHelmRelease("app-b")).To(BeNil())
	})
})
