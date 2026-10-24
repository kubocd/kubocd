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
package v1alpha1

import (
	kubocdv1alpha1 "kubocd/api/v1alpha1"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func protection(protected bool) *bool {
	return &protected
}

const (
	protectedDemo   = "release demo is protected"
	notComputedDemo = "release demo may be protected by its package, and the controller has not computed its protection " +
		"yet: set spec.protected to false to delete it anyway"
)

var _ = Describe("Release webhook", func() {
	DescribeTable("checks spec.protected, then the protection that the status of a deleted Release holds",
		func(spec *bool, printProtected string, refusal string) {
			release := &kubocdv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps"},
				Spec:       kubocdv1alpha1.ReleaseSpec{Protected: spec},
				Status:     kubocdv1alpha1.ReleaseStatus{Protected: printProtected == "X", PrintProtected: printProtected},
			}
			_, err := (&ReleaseCustomValidator{logger: logr.Discard()}).ValidateDelete(ctx, release)
			if refusal != "" {
				Expect(err).To(MatchError(refusal), "the deletion is not refused with %q", refusal)
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("spec true, protection not computed yet", protection(true), "", protectedDemo),
		Entry("spec true, status protected (control)", protection(true), "X", protectedDemo),
		Entry("spec false, protection not computed yet", protection(false), "", ""),
		Entry("spec false, status not computed again since spec was true", protection(false), "X", ""),
		Entry("spec false, status unprotected (control)", protection(false), "-", ""),
		Entry("spec unset, protection not computed yet", nil, "", notComputedDemo),
		Entry("spec unset, status protected", nil, "X", protectedDemo),
		Entry("spec unset, status unprotected", nil, "-", ""),
	)

	// The suite installs the webhook configurations of config/webhook, and runs
	// no controller: the status of a Release stays empty.
	It("refuses the deletion of a Release whose spec protects it, before any status", func() {
		release := &kubocdv1alpha1.Release{
			ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "default"},
			Spec: kubocdv1alpha1.ReleaseSpec{
				Protected: protection(true),
				Package:   kubocdv1alpha1.PackageSourceSpec{Repository: "registry.example/packages/demo", Tag: "0.1.0"},
			},
		}
		Expect(k8sClient.Create(ctx, release)).To(Succeed())
		Expect(k8sClient.Delete(ctx, release)).To(MatchError(ContainSubstring("release protected is protected")))
		key := types.NamespacedName{Namespace: "default", Name: "protected"}
		Expect(k8sClient.Get(ctx, key, release)).To(Succeed())

		release.Spec.Protected = protection(false)
		Expect(k8sClient.Update(ctx, release)).To(Succeed())
		Expect(k8sClient.Delete(ctx, release)).To(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, release))).To(BeTrue())
	})

	It("refuses the deletion of a Release whose protection is not computed, until spec.protected is false", func() {
		release := &kubocdv1alpha1.Release{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown", Namespace: "default"},
			Spec: kubocdv1alpha1.ReleaseSpec{
				Package: kubocdv1alpha1.PackageSourceSpec{Repository: "registry.example/packages/missing", Tag: "0.0.0"},
			},
		}
		Expect(k8sClient.Create(ctx, release)).To(Succeed())
		Expect(k8sClient.Delete(ctx, release)).To(MatchError(ContainSubstring("release unknown may be protected by its "+
			"package, and the controller has not computed its protection yet: set spec.protected to false to delete it anyway")),
			"the deletion of a Release whose protection is not computed is not refused")
		key := types.NamespacedName{Namespace: "default", Name: "unknown"}
		Expect(k8sClient.Get(ctx, key, release)).To(Succeed())

		release.Spec.Protected = protection(false)
		Expect(k8sClient.Update(ctx, release)).To(Succeed())
		Expect(k8sClient.Delete(ctx, release)).To(Succeed())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, release))).To(BeTrue())
	})
})
