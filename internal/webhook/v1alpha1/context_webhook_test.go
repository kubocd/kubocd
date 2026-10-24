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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The suite installs the webhook configurations of config/webhook in the API
// server, so the deletions below go through the webhook.
var _ = Describe("Context webhook", func() {
	It("refuses the deletion of a protected Context", func() {
		kcontext := &kubocdv1alpha1.Context{
			ObjectMeta: metav1.ObjectMeta{Name: "protected", Namespace: "default"},
			Spec:       kubocdv1alpha1.ContextSpec{Protected: true},
		}
		Expect(k8sClient.Create(ctx, kcontext)).To(Succeed())
		Expect(k8sClient.Delete(ctx, kcontext)).To(MatchError(ContainSubstring("context protected is protected")))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "protected"}, kcontext)).To(Succeed())

		kcontext.Spec.Protected = false
		Expect(k8sClient.Update(ctx, kcontext)).To(Succeed())
		Expect(k8sClient.Delete(ctx, kcontext)).To(Succeed())
	})

	It("deletes a Context that is not protected", func() {
		kcontext := &kubocdv1alpha1.Context{ObjectMeta: metav1.ObjectMeta{Name: "unprotected", Namespace: "default"}}
		Expect(k8sClient.Create(ctx, kcontext)).To(Succeed())
		Expect(k8sClient.Delete(ctx, kcontext)).To(Succeed())
		err := k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: "unprotected"}, kcontext)
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})
