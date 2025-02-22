package global

const ServiceApiVersion = "v1alpha1"

// Type of module for Service

const HelmChartType = "HelmChart"
const ServiceType = "Service"

// Env variable for OCI authentication

const OciUserEnvVar = "KCD_OCI_USER"
const OciSecretEnvVar = "KCD_OCI_SECRET"

const DockerCredentialHelperEnvVar = "KCD_DOCKER_CREDENTIAL_HELPER"

// --------------------- Media type in KuboCD Service image file

const ServiceModuleContentMediaType = "application/vnd.kubotal.kubocd.service.module.%s.content.v1.tar+gzip"

const ServiceManifestMediaType = "application/vnd.kubotal.kubocd.service.manifest.v1alpha1.tar+gzip"

const ServiceConfigMediaType = "application/vnd.kubotal.kubocd.service.config.v1alpha1+json"

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
