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

type ConnectionSpec struct {

	// Define the service type and allow validation of rendered 'values'
	// +kubebuilder:validation:Required
	Interface string `json:"interface"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=100
	Priority int `json:"priority"`

	// Must comply to the schema defined by interface
	// +kubebuilder:validation:Optional
	Values *apiextensionsv1.JSON `json:"values"`

	// For managed connections, the output name (Set by the release controller, at creation)
	// +kubebuilder:validation:Optional
	OutputName string `json:"outputName,omitempty"`

	// A human oriented description
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`
}

// const ConnectionKind = "Connection"

type ConnectionStatus struct {
	Phase ConnectionPhase `json:"phase"`
	// Name of the owner release (In case of managed connection). To be displayed to the user
	// +kubebuilder:validation:Optional
	Parent string `json:"parent,omitempty"`
	// +kubebuilder:validation:Optional
	Message string `json:"message,omitempty"`
	// InterfaceGeneration is the .metadata.generation of the interface this
	// connection was last checked against.
	// +kubebuilder:validation:Optional
	InterfaceGeneration int64 `json:"interfaceGeneration,omitempty"`
	// Interface or ClusterInterface. For user usage
	// +kubebuilder:validation:Optional as empty in initial state
	InterfaceKind Kind `json:"interfaceKind,omitempty"`
	// Decorated interface name, for user display
	// +kubebuilder:validation:Optional as empty in initial state
	InterfaceDisplay string `json:"interfaceDisplay,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cnx
// +kubebuilder:printcolumn:name="Interface",type=string,JSONPath=`.status.interfaceDisplay`
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Pri.",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Parent Rel.",type=string,JSONPath=`.status.parent`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec,omitempty"`
	Status ConnectionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Connection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Connection{}, &ConnectionList{})
}

// ------------------------------------------------------------------- ConnectionFacade

var _ ConnectionFacade = &Connection{}

func (cnx *Connection) GetKind() Kind {
	return KindConnection
}

func (cnx *Connection) GetValues() *apiextensionsv1.JSON {
	return cnx.Spec.Values
}

func (cnx *Connection) GetValuesRaw() []byte {
	if cnx.Spec.Values == nil {
		return nil
	}
	return cnx.Spec.Values.Raw
}

func (cnx *Connection) GetInterface() string {
	return cnx.Spec.Interface
}

func (cnx *Connection) GetOutputName() string {
	return cnx.Spec.OutputName
}

func (cnx *Connection) GetPriority() int {
	return cnx.Spec.Priority
}

func (cnx *Connection) GetStatusPhase() ConnectionPhase {
	return cnx.Status.Phase
}

func (cnx *Connection) GetStatusMessage() string {
	return cnx.Status.Message
}

func (cnx *Connection) GetParent() string {
	return cnx.Status.Parent
}
