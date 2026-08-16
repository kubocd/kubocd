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

type ParentReleaseRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ClusterConnectionSpec struct {

	// For managed clusterConnection.
	ParentRelease *ParentReleaseRef `json:"parentRelease,omitempty"`

	// Define the service type and allow validation of rendered 'values'
	// +kubebuilder:validation:Required
	Contract string `json:"contract"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=100
	Priority int `json:"priority"`

	// Must comply to the schema defined by contract
	// +kubebuilder:validation:Optional
	Values *apiextensionsv1.JSON `json:"values"`

	// For managed connections, the output name (Set by the release controller, at creation)
	// +kubebuilder:validation:Optional
	OutputName string `json:"outputName,omitempty"`

	// A human oriented description
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

//const ClusterConnectionKind = "ClusterConnection"

type ClusterConnectionStatus struct {
	Phase ConnectionPhase `json:"phase"`
	// Name of the owner release (In case of managed connection). To be displayed to the user
	// +optional
	Parent string `json:"parent,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// ContractGeneration is the .metadata.generation of the contract this
	// connection was last checked against.
	// +optional
	ContractGeneration int64 `json:"contractGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ccnx
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contract`
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Pri.",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Parent Rel.",type=string,JSONPath=`.status.parent`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type ClusterConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterConnectionSpec   `json:"spec,omitempty"`
	Status ClusterConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ClusterConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ClusterConnection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterConnection{}, &ClusterConnectionList{})
}

// ------------------------------------------------------------------- ConnectionFacade

var _ ConnectionFacade = &ClusterConnection{}

func (cnx *ClusterConnection) GetKind() Kind {
	return KindClusterConnection
}

//func (cnx *ClusterConnection) GetValues() *apiextensionsv1.JSON {
//	return cnx.Spec.Values
//}

func (cnx *ClusterConnection) GetValuesRaw() []byte {
	if cnx.Spec.Values == nil {
		return nil
	}
	return cnx.Spec.Values.Raw
}

func (cnx *ClusterConnection) GetContract() string {
	return cnx.Spec.Contract
}

func (cnx *ClusterConnection) GetOutputName() string {
	return cnx.Spec.OutputName
}

func (cnx *ClusterConnection) GetPriority() int {
	return cnx.Spec.Priority
}

func (cnx *ClusterConnection) GetStatusPhase() ConnectionPhase {
	return cnx.Status.Phase
}
