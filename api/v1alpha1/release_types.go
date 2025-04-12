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

type PackageSource struct {
	// Part of OCI url oci://<repository>:<tag>
	// +kubebuilder:validation:Required
	Repository string `json:"repository"`

	// Part of OCI url oci://<repository>:<tag>
	// +kubebuilder:validation:Required
	Tag string `json:"tag"`

	OciAddOn `json:",inline"`

	// Interval at which the OCIRepository URL is checked for updates.
	// This interval is approximate and may be subject to jitter to ensure
	// efficient use of resources.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m|h))+$"
	// +kubebuilder:default="5m"
	// +required
	Interval metav1.Duration `json:"interval"`

	// The timeout for remote OCI Repository operations like pulling, defaults to 60s.
	// +kubebuilder:default="60s"
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m))+$"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

type ReleaseDebug struct {

	// DumpContext instruct to save a representation of the context
	// in the Status. This for user debugging?
	// +kubebuilder:validation:Optional
	DumpContext bool `json:"dumpContext,omitempty"`

	// DumpParameters instruct to save a representation of the parameters
	// in the Status. This for user debugging?
	// +kubebuilder:validation:Optional
	DumpParameters bool `json:"dumpParameters,omitempty"`
}

// ReleaseSpec defines the desired state of Release.
type ReleaseSpec struct {

	// Short description of this release. Single line only
	// +kubebuilder:validation:Optional
	Description string `json:"description,omitempty"`

	// The package to deploy
	// +kubebuilder:validation:Required
	Package PackageSource `json:"package"`

	// To provide contextual variables
	// Refer to Context resource description for some explanation
	// Contexts are merged in the following order:
	// - The global default one (defined in Config)
	// - The namespace context (A context with a specific name, defined in config, present in the release namespace)
	// - This ordered list
	// +kubebuilder:validation:Optional
	// Default: []
	Contexts []NamespacedName `json:"contexts,omitempty"`

	// If true, HelmRelease update is suspended at KuboCD level
	// (This is NOT the helmRelease.spec.suspend flag, which may be set by Config part)
	// +kubebuilder:validation:Optional
	// Default: false
	Suspended bool `json:"suspended,omitempty"`

	// If true, the webhook will prevent deletion
	// TODO: Ensure some fallback if no webhook (Break ownership of helmReleases ?)
	// +kubebuilder:validation:Optional
	// Default: false
	Protected *bool `json:"protected,omitempty"`

	// The Release configuration variables
	// +kubebuilder:validation:Optional
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`

	// Allow to patch the HelmRelease.spec for each module
	// +kubebuilder:validation:Optional
	SpecPatchByModule map[string]*apiextensionsv1.JSON `json:"specPatchByModule,omitempty"`

	// If true, add  { install: { createNamespace: true } } to config map.
	// Must be set, as used in module.Render()
	// +kubebuilder:validation:Optional
	// Default: false
	CreateNamespace bool `json:"createNamespace"`

	// The namespace to deploy in. (May also be a partial name for a multi-namespaces package)
	// Not required, as it can be setup another way, depending on the package
	// (i.e. the package has a fixed namespace, or several ones).
	// +kubebuilder:validation:Optional
	// Default: Release.metadata.namespace
	TargetNamespace string `json:"targetNamespace,omitempty"`

	// List of roles fulfilled by this release. (appended to the one of the underlying package)
	// +kubebuilder:validation:Optional
	// Default: []
	Roles []string `json:"roles,omitempty"`

	// The roles we depend on. (appended to the one of the underlying package)
	// +kubebuilder:validation:Optional
	// Default: []
	Dependencies []string `json:"dependencies,omitempty"`

	// If yes, the default context(s) of the configs are not taken in account
	// +kubebuilder:validation:Optional
	//,Default: false
	SkipDefaultContext bool `json:"skipDefaultContext,omitempty"`

	// Group a set of parameters useful for debugging Release and Package
	// +kubebuilder:validation:Optional
	Debug *ReleaseDebug `json:"debug,omitempty"`
}

type ReleasePhase string

const ReleasePhaseReady = ReleasePhase("READY")
const ReleasePhaseError = ReleasePhase("ERROR")
const ReleasePhaseWaitOci = ReleasePhase("WAIT_OCI")
const ReleasePhaseWaitHelmRepo = ReleasePhase("WAIT_REPO")
const ReleasePhaseWaitHelmReleases = ReleasePhase("WAIT_HREL")
const ReleasePhaseWaitDependencies = ReleasePhase("WAIT_DEPS")
const ReleasePhaseSuspended = ReleasePhase("SUSPENDED")

// HelmReleaseState describe the observed state of a child HelmRelease
type HelmReleaseState struct {
	Ready  metav1.ConditionStatus `json:"ready"`
	Status string                 `json:"status,omitempty"`
}

// ReleaseStatus defines the observed state of Release.
// As we want Status to be explicit about provided information, we don't use 'omitempty' in its definition.
// (Except for 'context', as controlled by a debug flag)
type ReleaseStatus struct {
	Phase ReleasePhase `json:"phase"`

	// PrintContextsContexts is a string to list our context. Not technically used, but intended to be displayed
	// as printcolumn
	// +kubebuilder:validation:Optional
	PrintContexts string `json:"printContexts"`

	// PrintDescription
	// Copy of the release description, or, if empty the (templated) package one
	// +kubebuilder:validation:Optional
	PrintDescription string `json:"printDescription"`

	// Context is the resulting context, if requested in debug options
	// +kubebuilder:validation:Optional
	Context *apiextensionsv1.JSON `json:"context,omitempty"`

	// Parameters is the resulting parameters set, if requested in debug options
	// +kubebuilder:validation:Optional
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`

	// Usage is the rendering of the Package.spec.usage[key]. Aimed to provide user information.
	// Key could 'html', 'text', some language id, etc...
	// +kubebuilder:validation:Optional
	Usage map[string]string `json:"usage"`

	// Protected result of Release.spec.protected defaulted to package.spec.protected
	Protected bool `json:"protected"`

	// HelmReleaseState describe the observed state of child HelmReleases by name
	// +kubebuilder:validation:Optional
	HelmReleaseStates map[string]HelmReleaseState `json:"helmReleaseStates"`

	// ReadyReleases is a string to display X/Y helmRelease ready. Not technically used, but intended to be displayed
	// as printcolumn
	ReadyReleases string `json:"readyReleases"`

	// The result of the package template and release value
	Dependencies []string `json:"dependencies"`

	// The result of the package template and release value
	Roles []string `json:"roles"`

	MissingDependency string `json:"missingDependency"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.package.repository`
// +kubebuilder:printcolumn:name="Tag",type=string,JSONPath=`.spec.package.tag`
// +kubebuilder:printcolumn:name="Contexts",type=string,JSONPath=`.status.printContexts`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.readyReleases`
// +kubebuilder:printcolumn:name="Waiting",type=string,JSONPath=`.status.missingDependency`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.status.printDescription`

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
