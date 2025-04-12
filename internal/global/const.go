package global

const PackageApiVersion = "v1alpha1"

// Type of module for Package

const HelmChartType = "HelmChart"
const PackageType = "Package"

// Env variable for OCI authentication

const OciUserEnvVar = "KCD_OCI_USER"
const OciSecretEnvVar = "KCD_OCI_SECRET"

const DockerCredentialHelperEnvVar = "KCD_DOCKER_CREDENTIAL_HELPER"

// --------------------- Media type in KuboCD Package image file

const PackageContentMediaType = "application/vnd.kubotal.kubocd.application.content.v1.tar+gzip"

const PackageConfigMediaType = "application/vnd.kubotal.kubocd.application.config.v1alpha1+json"

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
