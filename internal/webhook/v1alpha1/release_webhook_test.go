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

var _ = Describe("Release webhook", func() {
	DescribeTable("checks spec.protected before the status of a deleted Release",
		func(spec *bool, status bool, refused bool) {
			release := &kubocdv1alpha1.Release{
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "apps"},
				Spec:       kubocdv1alpha1.ReleaseSpec{Protected: spec},
				Status:     kubocdv1alpha1.ReleaseStatus{Protected: status},
			}
			_, err := (&ReleaseCustomValidator{logger: logr.Discard()}).ValidateDelete(ctx, release)
			if refused {
				Expect(err).To(MatchError("release demo is protected"))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("spec true, status not computed yet", protection(true), false, true),
		Entry("spec true, status true (control)", protection(true), true, true),
		Entry("spec false, status not computed again since spec was true", protection(false), true, false),
		Entry("spec false, status false (control)", protection(false), false, false),
		Entry("spec unset, status true (control)", nil, true, true),
		Entry("spec unset, status false (control)", nil, false, false),
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
})
