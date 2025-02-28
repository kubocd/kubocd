
{{/*
Create a default fully qualified app name, to use as base bame for all ressources.
Use the release name by default
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
*/}}
{{- define "kubocd-controller.baseName" -}}
{{- if .Values.baseNameOverride }}
{{- .Values.baseNameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kubocd-controller.chartName" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubocd-controller.labels" -}}
helm.sh/chart: {{ include "kubocd-controller.chartName" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{/*
Controller Selector labels
*/}}
{{- define "kubocd-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubocd-controller.baseName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the deployment to use
*/}}
{{- define "kubocd-controller.deploymentName" -}}
{{- default (printf "%s" (include "kubocd-controller.baseName" .)) .Values.deploymentName }}
{{- end }}

{{/* --------------------------------------------------------------------- rbac */}}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubocd-controller.serviceAccountName" -}}
{{- default (printf "%s" (include "kubocd-controller.baseName" .)) .Values.serviceAccountName }}
{{- end }}

{{/*
Create the name of the associated role
*/}}
{{- define "kubocd-controller.clusterRoleName" -}}
{{- default (printf "%s" (include "kubocd-controller.baseName" .)) .Values.clusterRoleName }}
{{- end }}

{{/* --------------------------------------------------------------------- webhook */}}

{{/*
Create the name of the self-signed issuer for webhook certficate
*/}}
{{- define "kubocd-controller.webhook.certificateSelfSignedIssuerName" -}}
{{- default (printf "%s-webhook" (include "kubocd-controller.baseName" .)) .Values.webhook.certificateSelfSignedIssuerName }}
{{- end }}

{{/*
Create the name of the webhook services
*/}}
{{- define "kubocd-controller.webhook.serviceName" -}}
{{- default (printf "%s-webhook" (include "kubocd-controller.baseName" .)) .Values.webhook.serviceName }}
{{- end }}

{{/*
Create the name of the webhook tls certificate
*/}}
{{- define "kubocd-controller.webhook.certificateName" -}}
{{- default (printf "%s-webhook" (include "kubocd-controller.baseName" .)) .Values.webhook.certificateName }}
{{- end }}

{{/*
Create the name of the webhook tls secret
*/}}
{{- define "kubocd-controller.webhook.secretName" -}}
{{- default (printf "%s-webhook" (include "kubocd-controller.baseName" .)) .Values.webhook.secretName }}
{{- end }}

{{/*
Create the name of the validating webhook configuration
*/}}
{{- define "kubocd-controller.webhook.validatingWebhookConfiguration" -}}
{{- default (printf "%s-validating-webhook-configuration" (include "kubocd-controller.baseName" .)) .Values.webhook.validatingWebhookConfiguration }}
{{- end }}

{{/*
Create the name of the mutating webhook configuration
*/}}
{{- define "kubocd-controller.webhook.mutatingWebhookConfiguration" -}}
{{- default (printf "%s-mutating-webhook-configuration" (include "kubocd-controller.baseName" .)) .Values.webhook.mutatingWebhookConfiguration }}
{{- end }}

{{/* --------------------------------------------------------------------- metrics */}}

{{/*
Create the name of the metrics services
*/}}
{{- define "kubocd-controller.metrics.serviceName" -}}
{{- default (printf "%s-metrics" (include "kubocd-controller.baseName" .)) .Values.metrics.serviceName }}
{{- end }}

{{/*
Create the name of the self-signed issuer for metrics certficate
*/}}
{{- define "kubocd-controller.metrics.certificateSelfSignedIssuerName" -}}
{{- default (printf "%s-metrics" (include "kubocd-controller.baseName" .)) .Values.metrics.certificateSelfSignedIssuerName }}
{{- end }}

{{/*
Create the name of the metrics tls certificate
*/}}
{{- define "kubocd-controller.metrics.certificateName" -}}
{{- default (printf "%s-metrics" (include "kubocd-controller.baseName" .)) .Values.metrics.certificateName }}
{{- end }}

{{/*
Create the name of the metrics tls secret
*/}}
{{- define "kubocd-controller.metrics.secretName" -}}
{{- default (printf "%s-metrics" (include "kubocd-controller.baseName" .)) .Values.metrics.secretName }}
{{- end }}


{{/*
Create the name of the metrics serviceMonitor
*/}}
{{- define "kubocd-controller.metrics.serviceMonitor.name" -}}
{{- default (printf "%s" (include "kubocd-controller.baseName" .)) .Values.metrics.serviceMonitor.name }}
{{- end }}

{{/* --------------------------------------------------------------------- helm-respitory */}}

{{/*
Create the name of the helm repository services
*/}}
{{- define "kubocd-controller.helm-repository.serviceName" -}}
{{- default (printf "%s-helm-repository" (include "kubocd-controller.baseName" .)) .Values.helmRepository.serviceName }}
{{- end }}


