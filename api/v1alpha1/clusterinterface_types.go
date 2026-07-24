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

type ClusterInterfaceSpec struct {

	// Allow validation of the 'values' attribute on Release.output[x]
	// May be a JSON/openAPI schema or the kubocd simplified schema format.
	// +kubebuilder:validation:Optional
	Schema *apiextensionsv1.JSON `json:"schema"`

	// A human oriented description
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

type ClusterInterfaceStatus struct {
	Phase InterfacePhase `json:"phase"`
	// +optional
	// +kubebuilder:validation:Optional
	Message string `json:"message"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ciface
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type ClusterInterface struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InterfaceSpec   `json:"spec,omitempty"`
	Status            InterfaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ClusterInterfaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterInterface `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterInterface{}, &ClusterInterfaceList{})
}

// ------------------------------------------------------

var _ InterfaceFacade = &ClusterInterface{}

func (cIface *ClusterInterface) GetKind() Kind {
	return KindClusterInterface
}

func (cIface *ClusterInterface) GetSchemaRaw() []byte {
	if cIface.Spec.Schema == nil {
		return nil
	}
	return cIface.Spec.Schema.Raw
}

func (cIface *ClusterInterface) GetStatusPhase() InterfacePhase {
	return cIface.Status.Phase
}
