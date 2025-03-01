package global

const ApplicationApiVersion = "v1alpha1"

// Type of module for Application

const HelmChartType = "HelmChart"
const ApplicationType = "Application"

// Env variable for OCI authentication

const OciUserEnvVar = "KCD_OCI_USER"
const OciSecretEnvVar = "KCD_OCI_SECRET"

const DockerCredentialHelperEnvVar = "KCD_DOCKER_CREDENTIAL_HELPER"

// --------------------- Media type in KuboCD Application image file

const ApplicationContentMediaType = "application/vnd.kubotal.kubocd.application.content.v1.tar+gzip"

const ApplicationConfigMediaType = "application/vnd.kubotal.kubocd.application.config.v1alpha1+json"

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
