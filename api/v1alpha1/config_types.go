/*
Copyright 2025 Kubotal

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

type PackageRedirectSpec struct {

	// All KuboCD package repository where the Url begin by this value will be impacted.
	// +kubebuilder:validation:Required
	OldPrefix string `json:"oldPrefix"`

	// The prefix part will be replaced by this value.
	// +kubebuilder:validation:Required
	NewPrefix string `json:"newPrefix"`

	OciAddOn `json:",inline"`

	// Interval at which the OCIRepository URL is checked for updates.
	// This interval is approximate and may be subject to jitter to ensure
	// efficient use of resources.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m|h))+$"
	// +optional
	Interval metav1.Duration `json:"interval"`

	// The timeout for remote OCI Repository operations like pulling
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ms|s|m))+$"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`
}

type ImageRedirectSpec struct {

	// All image repo where the Url begin by this value will be impacted.
	// +kubebuilder:validation:Required
	OldPrefix string `json:"oldPrefix"`

	// The prefix part will be replaced by this value.
	// +kubebuilder:validation:Required
	NewPrefix string `json:"newPrefix"`

	// +kubebuilder:validation:Enum=IfNotPresent;Always;Never
	// +kubebuilder:validation:Optional
	ImagePullPolicy string `json:"imagePullPolicy"`

	// +kubebuilder:validation:Optional
	ImagePullSecrets []string `json:"imagePullSecrets"`
}

type OnFailureStrategySpec struct {

	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// +kubebuilder:validation:Optional
	Strategy *apiextensionsv1.JSON `json:"strategy,omitempty"`
}

// ConfigSpec defines the desired state of Config.
type ConfigSpec struct {

	// This will apply to KuboCD package oci image.
	// Each entry allow substitution of oci data source.
	// Aim is to ease handling of Air Gap deployment, to replace public repo be an internal ones.
	// This will also allow to add authentication and proxy information.
	// When merging Configs, values are simply appended. (Configs are sorted by name)
	// +kubebuilder:validation:Optional
	// Default: []
	PackageRedirects []*PackageRedirectSpec `json:"packageRedirects,omitempty"`

	// This may apply to image referenced in Helm Chart
	// It is intended to be used by some templating functions, inserted in the `values` template of the package
	// Each entry allow substitution of oci data source.
	// Aim is to ease handling of Air Gap deployment, to replace public repo be an internal ones.
	// This will also allow to add authentication information.
	// When merging Configs, values are simply appended. (Configs are sorted by name)
	// +kubebuilder:validation:Optional
	// Default: []
	ImageRedirects []*ImageRedirectSpec `json:"imageRedirects,omitempty"`

	// Allow to define Roles already provided by the k8s cluster, independently of any KuboCD deployment.
	// When merging, values are appended
	// +kubebuilder:validation:Optional
	// Default: []
	ClusterRoles []string `json:"clusterRoles,omitempty"`

	// Define context which will be added to all release, except the one with 'skipDefaultContext' flag
	// +kubebuilder:validation:Optional
	// Default: []
	DefaultContexts []NamespacedName `json:"defaultContexts,omitempty"`

	// Define a list of context name which will be added to all releases located in a namespace,
	// except the one with 'skipDefaultContext' flag
	DefaultNamespaceContexts []string `json:"defaultNamespaceContexts,omitempty"`

	// +kubebuilder:validation:Optional
	OnFailureStrategies []*OnFailureStrategySpec `json:"onFailureStrategies,omitempty"`

	// +kubebuilder:validation:Optional
	DefaultOnFailureStrategy string `json:"defaultOnFailureStrategy,omitempty"`

	// Value to set to helmRelease.spec.timeout
	// Default: 2mn
	// +kubebuilder:validation:Optional
	DefaultHelmTimeout *metav1.Duration `json:"defaultHelmTimeout,omitempty"`

	// Value to set to helmRelease.spec.interval (interval at which to reconcile the Helm release)
	// Default: 2mn
	// +kubebuilder:validation:Optional
	DefaultHelmInterval *metav1.Duration `json:"defaultHelmInterval,omitempty"`

	// Default value for Release.spec.package.interval. Interval applied to the OciRepository, to check against new package version
	// Default: 30mn
	// +kubebuilder:validation:Optional
	DefaultPackageInterval *metav1.Duration `json:"defaultPackageInterval,omitempty"`

	// Allow to patch the HelmRelease.spec for all modules
	// +kubebuilder:validation:Optional
	SpecPatch *apiextensionsv1.JSON `json:"specPatch,omitempty"`
}

// ConfigStatus defines the observed state of Config.
type ConfigStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=kconfig;kconf
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Config is the Schema for the Configs API.
type Config struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigSpec   `json:"spec,omitempty"`
	Status ConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConfigList contains a list of Config.
type ConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Config `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Config{}, &ConfigList{})
}
