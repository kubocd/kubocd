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

type ClusterContractSpec struct {

	// Allow validation of the 'values' attribute on Release.output[x]
	// May be a JSON/openAPI schema or the kubocd simplified schema format.
	// +kubebuilder:validation:Optional
	Schema *apiextensionsv1.JSON `json:"schema"`

	// A human oriented description
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

type ClusterContractStatus struct {
	Phase ContractPhase `json:"phase"`
	// +optional
	// +kubebuilder:validation:Optional
	Message string `json:"message"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cct
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type ClusterContract struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterContractSpec   `json:"spec,omitempty"`
	Status            ClusterContractStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ClusterContractList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterContract `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterContract{}, &ClusterContractList{})
}

// ------------------------------------------------------

var _ ContractFacade = &ClusterContract{}

func (cContract *ClusterContract) GetKind() Kind {
	return KindClusterContract
}

func (cContract *ClusterContract) GetSchemaRaw() []byte {
	if cContract.Spec.Schema == nil {
		return nil
	}
	return cContract.Spec.Schema.Raw
}

func (cContract *ClusterContract) GetStatusPhase() ContractPhase {
	return cContract.Status.Phase
}
