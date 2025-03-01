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

type OciRedirectSpec struct {

	// All OIC repo where the Url begin by this value will be impacted.
	// +kubebuilder:validation:Required
	OldRepositoryPrefix string `json:"oldRepositoryPrefix"`

	// The prefix part will be replaced by this value.
	// +kubebuilder:validation:Required
	NewRepositoryPrefix string `json:"newRepositoryPrefix"`

	ApplicationSourceAddOn `json:",inline"`
}

// SettingSpec defines the desired state of Setting.
type SettingSpec struct {

	// Short description. Single line only
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// If true, to be used as a parent and not candidate to be referenced from a Release.
	// This is NOT enforced by the controller, but just for the usage of a potential front end
	// +kubebuilder:validation:Optional
	// Default: false
	Abstract bool `json:"abstract,omitempty"`

	// Settings can be merged together.
	// See each property description for more detail.
	// +kubebuilder:validation:Optional
	// Default: []
	Parents []NamespacedNameSpec `json:"parents,omitempty"`

	// Context is a map of variables witches will be injected in the data model when rendering Application template.
	// When merging setting, context merge is performed by patching 'oldest' one with the 'newest' one.
	// The 'oldest' are the deeper in the parents chain.
	// Then merging is performed with the parent array order.
	// +kubebuilder:validation:Optional
	Context *apiextensionsv1.JSON `json:"context,omitempty"`

	// Each entry allow substitution of oci data source.
	// Aim is to ease handling of Air Gap deployment, to replace public repo be an internal ones.
	// This will also allow to add authentication and proxy information.
	// When merging settings, values are simply appended.
	// +kubebuilder:validation:Optional
	OciRedirects []OciRedirectSpec `json:"ociRedirects,omitempty"`

	// Allow to define Roles already provided by the k8s cluster, independently of any KuboCD deployment.
	// +kubebuilder:validation:Optional
	// Default: []
	ClusterRoles []string `json:"clusterRoles,omitempty"`
}

// SettingStatus defines the observed state of Setting.
type SettingStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Setting is the Schema for the settings API.
type Setting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SettingSpec   `json:"spec,omitempty"`
	Status SettingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SettingList contains a list of Setting.
type SettingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Setting `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Setting{}, &SettingList{})
}
