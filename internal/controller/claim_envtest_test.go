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

//nolint:goconst // Keep claim cases readable.
package controller

import (
	kv1 "kubocd/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Claim validation", func() {
	It("needs exactly one target, keeps its contract and target, and takes a status written first by another tool", func(ctx SpecContext) {
		const ns = "claim-validation"
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})).To(Succeed())
		claim := func(name string, target kv1.ClaimTarget) *kv1.Claim {
			return &kv1.Claim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: kv1.ClaimSpec{Contract: "database-v1", Target: target}}
		}
		for name, target := range map[string]kv1.ClaimTarget{
			"none": {}, "both": {Provisioner: "postgresql", Connection: "ext-pg"},
		} {
			Expect(k8sClient.Create(ctx, claim(name, target))).To(
				MatchError(ContainSubstring("exactly one of provisioner and connection must be set")))
		}

		created := claim("db", kv1.ClaimTarget{Provisioner: "postgresql"})
		Expect(k8sClient.Create(ctx, created)).To(Succeed())
		retargeted := created.DeepCopy()
		retargeted.Spec.Target = kv1.ClaimTarget{Connection: "ext-pg"}
		Expect(k8sClient.Update(ctx, retargeted)).To(MatchError(ContainSubstring("target is immutable")))
		recontracted := created.DeepCopy()
		recontracted.Spec.Contract = "s3-v1"
		Expect(k8sClient.Update(ctx, recontracted)).To(MatchError(ContainSubstring("contract is immutable")))
		required := created.DeepCopy()
		required.Spec.Requirements = &apiextensionsv1.JSON{Raw: []byte(`{"serverVersion": ">=15"}`)}
		Expect(k8sClient.Update(ctx, required)).To(Succeed())
		Expect(required.Generation).To(Equal(int64(2)))

		written := &kv1.Claim{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(created), written)).To(Succeed())
		written.Status = kv1.ClaimStatus{Phase: "READY"}
		Expect(k8sClient.Status().Update(ctx, written)).To(MatchError(ContainSubstring("Unsupported value")))
		written.Status = kv1.ClaimStatus{Phase: kv1.ClaimPhaseUnbound, Message: "creating the database", ObservedGeneration: 2}
		Expect(k8sClient.Status().Update(ctx, written)).To(Succeed())
	})
})
