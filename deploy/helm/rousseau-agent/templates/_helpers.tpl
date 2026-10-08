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
Effective image reference. An empty tag derives from the flavour and
the chart's appVersion (`distroless-vX.Y.Z` or `vX.Y.Z`) — "install
this chart, get a validated image" semantics.
*/}}
{{- define "rousseau-agent.image" -}}
{{- $flavour := default "distroless" .Values.image.flavour -}}
{{- if not (has $flavour (list "distroless" "full")) -}}
{{- fail (printf "image.flavour must be \"distroless\" or \"full\", got %q" $flavour) -}}
{{- end -}}
{{- $prefix := ternary "distroless-" "" (eq $flavour "distroless") -}}
{{- $tag := default (printf "%s%s" $prefix .Chart.AppVersion) .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
True ("true") when something needs the metrics listener: a probe or
the ServiceMonitor.
*/}}
{{- define "rousseau-agent.metricsEnabled" -}}
{{- if or .Values.probes.liveness.enabled .Values.probes.readiness.enabled .Values.serviceMonitor.enabled -}}
true
{{- end -}}
{{- end -}}

{{/*
Rendered config.yaml: the user's config plus, when the metrics
listener is needed, observability.metrics_addr on service.metricsPort.
A user-set metrics_addr on another port is an error, so probes and
the ServiceMonitor always target the port the daemon serves.
*/}}
{{- define "rousseau-agent.config" -}}
{{- $cfg := deepCopy (default (dict) .Values.config) -}}
{{- if include "rousseau-agent.metricsEnabled" . -}}
{{- $want := printf ":%v" .Values.service.metricsPort -}}
{{- $obs := default (dict) (get $cfg "observability") -}}
{{- $have := default "" (get $obs "metrics_addr") -}}
{{- if and $have (not (hasSuffix $want $have)) -}}
{{- fail (printf "config.observability.metrics_addr %q does not end in %q (service.metricsPort); probes and the ServiceMonitor would target a port the daemon does not serve" $have $want) -}}
{{- end -}}
{{- if not $have -}}
{{- $_ := set $obs "metrics_addr" $want -}}
{{- end -}}
{{- $_ := set $cfg "observability" $obs -}}
{{- end -}}
{{- if $cfg -}}
{{- toYaml $cfg -}}
{{- end -}}
{{- end -}}
