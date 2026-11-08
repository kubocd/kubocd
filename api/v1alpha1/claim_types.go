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

package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClaimTarget names what binds the claim: a provisioner or an existing Connection.
// +kubebuilder:validation:XValidation:rule="has(self.provisioner) != has(self.connection)",message="exactly one of provisioner and connection must be set"
type ClaimTarget struct {
	// A provisioner, which creates the resource and the connection, then writes the status of the claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	Provisioner string `json:"provisioner,omitempty"`

	// An existing Connection, in the namespace of the claim
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	Connection string `json:"connection,omitempty"`
}

type ClaimSpec struct {
	// The contract of the connection
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="contract is immutable"
	Contract string `json:"contract"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="target is immutable"
	Target ClaimTarget `json:"target"`

	// Checked against the connection, as the claim section of the contract defines
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Type=object
	Requirements *apiextensionsv1.JSON `json:"requirements,omitempty"`
}

type ClaimPhase string

const ClaimPhaseBound = ClaimPhase("BOUND")
const ClaimPhaseUnbound = ClaimPhase("UNBOUND")

// ClaimStatus is written by KuboCD when the target is a connection, and by the provisioner otherwise.
// All its fields are optional, for the provisioner to write the first status.
type ClaimStatus struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=BOUND;UNBOUND
	Phase ClaimPhase `json:"phase,omitempty"`

	// The reason, when UNBOUND
	// +kubebuilder:validation:Optional
	Message string `json:"message,omitempty"`

	// The bound Connection, in the namespace of the claim
	// +kubebuilder:validation:Optional
	Connection string `json:"connection,omitempty"`

	// The generation of the claim this status was written for
	// +kubebuilder:validation:Optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=clm
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contract`
// +kubebuilder:printcolumn:name="Provisioner",type=string,JSONPath=`.spec.target.provisioner`
// +kubebuilder:printcolumn:name="Connection",type=string,JSONPath=`.spec.target.connection`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Bound",type=string,JSONPath=`.status.connection`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Claim requests a connection of a contract, from a provisioner or from an existing Connection.
type Claim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClaimSpec   `json:"spec,omitempty"`
	Status ClaimStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Claim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Claim{}, &ClaimList{})
}
