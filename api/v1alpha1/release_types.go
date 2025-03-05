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

type ApplicationSource struct {
	// Part of OCI url oci://<repository>:<tag>
	// +kubebuilder:validation:Required
	Repository string `json:"repository"`

	ApplicationSourceAddOn `json:",inline"`
}

type ReleaseDebug struct {

	// DumpContext instruct to save a representation of the context
	// in the Status. This for user debugging?
	// +kubebuilder:validation:Optional
	DumpContext bool `json:"dumpContext,omitempty"`
}

// ReleaseSpec defines the desired state of Release.
type ReleaseSpec struct {

	// Short description of this release. Single line only
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// The application to deploy
	// +kubebuilder:validation:Required
	Application ApplicationSource `json:"application"`

	// To provide contextual variables
	// Refer to Context resource description for more explanation
	// +kubebuilder:validation:Optional
	// Default: []
	Contexts []NamespacedName `json:"contexts,omitempty"`

	// If false, this release is not deployed (And deleted if existing and unprotected)
	// +kubebuilder:validation:Optional
	// Default: true
	Enabled *bool `json:"enabled,omitempty"`

	// If true, HelmRelease update is suspended at KuboCD level
	// (This is NOT the helmRelease.spec.suspend flag, which may be set by Config part)
	// +kubebuilder:validation:Optional
	// Default: false
	Suspended bool `json:"suspended,omitempty"`

	// If true, the webhook will prevent deletion
	// TODO: Ensure some fallback if no webhook (Break ownership of helmReleases ?)
	// +kubebuilder:validation:Optional
	// Default: false
	Protected bool `json:"protected,omitempty"`

	// The Release configuration variables
	// +kubebuilder:validation:Optional
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`

	// If true, add  { install: { createNamespace: true } } to config map.
	// +kubebuilder:validation:Optional
	// Default: false
	CreateNamespace bool `json:"createNamespace,omitempty"`

	// The namespace to deploy in. (May also be a partial name for a multi-namespaces application)
	// Not required, as it can be setup another way, depending on the application
	// (i.e the application has a fixed namespace, or several ones).
	// +kubebuilder:validation:Optional
	// Default: ""
	Namespace string `json:"namespace,omitempty"`

	// List of roles fulfilled by this release. (appended to the one of the underlying application)
	// +kubebuilder:validation:Optional
	// Default: []
	Roles []string `json:"roles,omitempty"`

	// The roles we depend on. (appended to the one of the underlying Application)
	// +kubebuilder:validation:Optional
	// Default: []
	DependsOn []string `json:"dependsOn,omitempty"`

	// Group a set of parameters useful for debugging Release and Application
	// +kubebuilder:validation:Optional
	Debug *ReleaseDebug `json:"debug,omitempty"`
}

type ReleasePhase string

const ReleasePhaseReady = ReleasePhase("READY")
const ReleasePhaseError = ReleasePhase("ERROR")
const ReleasePhaseWaitOci = ReleasePhase("WAIT_OCI")
const ReleasePhaseWaitHelmRepo = ReleasePhase("WAIT_HELM_REPO")

// ReleaseStatus defines the observed state of Release.
type ReleaseStatus struct {
	Phase ReleasePhase `json:"phase"`

	// Contexts is a string to list our context. Not technically used, but intended to be displayed
	// as printcolumn
	Contexts string `json:"contexts,omitempty"`

	// Context is the resulting context, if requested in debug options
	// +kubebuilder:validation:Optional
	Context *apiextensionsv1.JSON `json:"context,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.application.repository`
// +kubebuilder:printcolumn:name="Tag",type=string,JSONPath=`.spec.application.tag`
// +kubebuilder:printcolumn:name="Contexts",type=string,JSONPath=`.status.contexts`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Release is the Schema for the releases API.
type Release struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReleaseSpec   `json:"spec,omitempty"`
	Status ReleaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ReleaseList contains a list of Release.
type ReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Release `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Release{}, &ReleaseList{})
}
