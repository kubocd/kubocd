/*

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

// ContextSpec defines the desired state of Context.
type ContextSpec struct {

	// Short description. Single line only
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// If true, to be used as a parent and not candidate to be referenced from a Release.
	// This is NOT enforced by the controller, but just for the usage of a potential front end
	// +kubebuilder:validation:Optional
	// Default: false
	Abstract bool `json:"abstract,omitempty"`

	// Context can be merged together.
	// See each property description for more detail.
	// +kubebuilder:validation:Optional
	// Default: []
	Parents []NamespacedName `json:"parents,omitempty"`

	// If true, the webhook will prevent deletion
	// TODO: Ensure some fallback if no webhook (Break ownership of helmReleases ?)
	// +kubebuilder:validation:Optional
	// Default: false
	Protected bool `json:"protected,omitempty"`

	// Context is a map of variables witches will be injected in the data model when rendering Application template.
	// When merging setting, context merge is performed by patching 'oldest' one with the 'newest' one.
	// The 'oldest' are the deeper in the parents chain.
	// Then merging is performed with the parent array order.
	// +kubebuilder:validation:Optional
	Context *apiextensionsv1.JSON `json:"context,omitempty"`
}

type ContextPhase string

const ContextPhaseReady = ContextPhase("READY")
const ContextPhaseError = ContextPhase("ERROR")

// ContextStatus defines the observed state of Context.
type ContextStatus struct {
	Phase ContextPhase `json:"phase"`

	// Parents is a string to list our parents. Not technically used, but intended to be displayed
	// as printcolumn
	Parents string `json:"parents,omitempty"`

	// Context is the resulting context, after potential parent merging
	// if there is no parent, it is empty, so use the one from Spec.
	// +kubebuilder:validation:Optional
	Context *apiextensionsv1.JSON `json:"context,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ctx;kcontext;kctx
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Parents",type=string,JSONPath=`.status.parents`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Context is the Schema for the settings API.
type Context struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContextSpec   `json:"spec,omitempty"`
	Status ContextStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ContextList contains a list of Context.
type ContextList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Context `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Context{}, &ContextList{})
}
