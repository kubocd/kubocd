{{/*
Create a default fully qualified app name, to use as base bame for all webhook ressources.
*/}}
{{- define "kubocd.webhook.baseName" -}}
{{- if .Values.webhook.baseNameOverride }}
{{- .Values.webhook.baseNameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-webhook" (include "kubocd.baseName" .) }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubocd.webhook.labels" -}}
helm.sh/chart: {{ include "kubocd.chartName" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}


{{/*
webhook Selector labels
*/}}
{{- define "kubocd.webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubocd.webhook.baseName" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the deployment to use
*/}}
{{- define "kubocd.webhook.deploymentName" -}}
{{- default (printf "%s" (include "kubocd.webhook.baseName" .)) .Values.webhook.deploymentName }}
{{- end }}

{{/* --------------------------------------------------------------------- rbac */}}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubocd.webhook.serviceAccountName" -}}
{{- default (printf "%s" (include "kubocd.webhook.baseName" .)) .Values.webhook.serviceAccountName }}
{{- end }}

{{/*
Create the name of the associated role
*/}}
{{- define "kubocd.webhook.clusterRoleName" -}}
{{- default (printf "%s" (include "kubocd.webhook.baseName" .)) .Values.webhook.clusterRoleName }}
{{- end }}

{{/* --------------------------------------------------------------------- metrics */}}

{{/*
Create the name of the metrics services
*/}}
{{- define "kubocd.webhook.metrics.serviceName" -}}
{{- default (printf "%s-metrics" (include "kubocd.webhook.baseName" .)) .Values.webhook.metrics.serviceName }}
{{- end }}

{{/*
Create the name of the self-signed issuer for metrics certficate
*/}}
{{- define "kubocd.webhook.metrics.certificateSelfSignedIssuerName" -}}
{{- default (printf "%s-metrics" (include "kubocd.webhook.baseName" .)) .Values.webhook.metrics.certificateSelfSignedIssuerName }}
{{- end }}

{{/*
Create the name of the metrics tls certificate
*/}}
{{- define "kubocd.webhook.metrics.certificateName" -}}
{{- default (printf "%s-metrics" (include "kubocd.webhook.baseName" .)) .Values.webhook.metrics.certificateName }}
{{- end }}

{{/*
Create the name of the metrics tls secret
*/}}
{{- define "kubocd.webhook.metrics.secretName" -}}
{{- default (printf "%s-metrics" (include "kubocd.webhook.baseName" .)) .Values.webhook.metrics.secretName }}
{{- end }}


{{/*
Create the name of the metrics serviceMonitor
*/}}
{{- define "kubocd.webhook.metrics.serviceMonitor.name" -}}
{{- default (printf "%s" (include "kubocd.webhook.baseName" .)) .Values.webhook.metrics.serviceMonitor.name }}
{{- end }}


{{/* --------------------------------------------------------------------- webhook */}}

{{/*
Create the name of the self-signed issuer for webhook certficate
*/}}
{{- define "kubocd.webhook.certificateSelfSignedIssuerName" -}}
{{- default (printf "%s-webhook" (include "kubocd.webhook.baseName" .)) .Values.webhook.certificateSelfSignedIssuerName }}
{{- end }}

{{/*
Create the name of the webhook services
*/}}
{{- define "kubocd.webhook.serviceName" -}}
{{- default (printf "%s-webhook" (include "kubocd.webhook.baseName" .)) .Values.webhook.serviceName }}
{{- end }}

{{/*
Create the name of the webhook tls certificate
*/}}
{{- define "kubocd.webhook.certificateName" -}}
{{- default (printf "%s-webhook" (include "kubocd.webhook.baseName" .)) .Values.webhook.certificateName }}
{{- end }}

{{/*
Create the name of the webhook tls secret
*/}}
{{- define "kubocd.webhook.secretName" -}}
{{- default (printf "%s-webhook" (include "kubocd.webhook.baseName" .)) .Values.webhook.secretName }}
{{- end }}

{{/*
Create the name of the validating webhook configuration
*/}}
{{- define "kubocd.webhook.validatingWebhookConfiguration" -}}
{{- default (printf "%s-validating-webhook-configuration" (include "kubocd.webhook.baseName" .)) .Values.webhook.validatingWebhookConfiguration }}
{{- end }}

{{/*
Create the name of the mutating webhook configuration
*/}}
{{- define "kubocd.webhook.mutatingWebhookConfiguration" -}}
{{- default (printf "%s-mutating-webhook-configuration" (include "kubocd.webhook.baseName" .)) .Values.webhook.mutatingWebhookConfiguration }}
{{- end }}
