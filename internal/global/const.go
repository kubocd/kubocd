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

package global

const PackageApiVersion = "v1alpha1"

// Type of module for Package

const HelmChartType = "HelmChart"
const PackageType = "Package"

// Env variable for OCI authentication

const DeprecatedOciUserEnvVar = "KCD_OCI_USER"
const DeprecatedOciSecretEnvVar = "KCD_OCI_SECRET"

const OciUserEnvVarFormat = "KCD_OCI_%s_USER"
const OciSecretEnvVarFormat = "KCD_OCI_%s_SECRET"

const DockerCredentialHelperEnvVar = "KCD_DOCKER_CREDENTIAL_HELPER"

// --------------------- Media type in KuboCD Package image file

const PackageContentMediaType = "application/vnd.kubotal.kubocd.package.content.v1.tar+gzip"

const PackageConfigMediaType = "application/vnd.kubotal.kubocd.package.config.v1alpha1+json"

// --------------------- Media type in helm chart OCI image

const HelmChartMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

// ----------------------

const FinalizerName = "kubocd.kubotal.io/finalizer"

// Annotation in Artifact object

const ModuleNameAnnotation = "io.kubotal.kubocd.module.name"

// Files in image

//const ManifestYaml = "manifest.yaml"
//const ManifestJson = "manifest.json"

// Subfolders of rootDataFolder

const SourceFolder = "source"
const StorageFolder = "storage"
