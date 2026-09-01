{{/*
Expand the name of the chart.
*/}}
{{- define "rousseau-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified app name. Used for every generated object's name so
`helm install` picks a stable identifier even when the release name
collides with the chart name.
*/}}
{{- define "rousseau-agent.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart name + version, for the standard Helm chart label.
*/}}
{{- define "rousseau-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels — applied to every generated object.
*/}}
{{- define "rousseau-agent.labels" -}}
helm.sh/chart: {{ include "rousseau-agent.chart" . }}
{{ include "rousseau-agent.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels — used by the Service to find the Deployment's pods.
Kept minimal so Deployment updates don't churn the selector (the
selector is immutable once set).
*/}}
{{- define "rousseau-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rousseau-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Service account name — either the one the operator supplied or a
derived-from-release default.
*/}}
{{- define "rousseau-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "rousseau-agent.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Effective image reference. The values file allows an empty tag so
the chart's appVersion is the safe default — matches "install this
chart, get a validated image" semantics.
*/}}
{{- define "rousseau-agent.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
