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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type ReplicationSource struct {
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

type ReplicationDestination struct {
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace"`
	// Default to source name
	// +kubebuilder:validation:Optional
	Name string `json:"name"`
}

type ReplicationSpec struct {
	Source ReplicationSource `json:"source"`

	Destination ReplicationDestination `json:"destination"`
}

type ReplicationPhase string

const ReplicationPhaseReady ReplicationPhase = "READY"
const ReplicationPhaseError ReplicationPhase = "ERROR"

// ReplicatedResource identifies a resource which has been created by a Replication
type ReplicatedResource struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type ReplicationStatus struct {
	Phase ReplicationPhase `json:"phase"`
	// +optional
	// +kubebuilder:validation:Optional
	Message string `json:"message"`
	// The effective destination name, defaulted to the source name when not set in the spec.
	// Always set, as intended for display.
	// +optional
	// +kubebuilder:validation:Optional
	DestinationName string `json:"destinationName"`
	// The resource currently handled by this Replication. Allow its removal when the spec is modified
	// to target another resource, or when this Replication is deleted. Unset when nothing is replicated.
	// +optional
	// +kubebuilder:validation:Optional
	Destination *ReplicatedResource `json:"destination,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rep
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.source.kind`
// +kubebuilder:printcolumn:name="Src name",type=string,JSONPath=`.spec.source.name`
// +kubebuilder:printcolumn:name="Dst namespace",type=string,JSONPath=`.spec.destination.namespace`
// +kubebuilder:printcolumn:name="Dst name",type=string,JSONPath=`.status.destinationName`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.status.message`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

type Replication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReplicationSpec   `json:"spec,omitempty"`
	Status            ReplicationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ReplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Replication `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Replication{}, &ReplicationList{})
}
