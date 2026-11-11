{{/* The chart name, shortened to what Kubernetes allows. */}}
{{- define "view.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* The name for this release's objects: "view" when the release is called view. */}}
{{- define "view.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Cluster-wide objects get the namespace in their name, so two installs never collide. */}}
{{- define "view.clusterName" -}}
{{- printf "%s-%s" .Release.Namespace (include "view.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "view.selectorLabels" -}}
app.kubernetes.io/name: {{ include "view.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "view.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "view.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* "true" when the network card is switched on (network.enabled), otherwise empty. */}}
{{- define "view.networkEnabled" -}}
{{- if and (kindIs "map" .Values.network) (eq (toString .Values.network.enabled) "true") -}}true{{- end -}}
{{- end -}}

{{/* "true" when the platform footer is switched on (tooling.enabled), otherwise empty. */}}
{{- define "view.toolingEnabled" -}}
{{- if and (kindIs "map" .Values.tooling) (eq (toString .Values.tooling.enabled) "true") -}}true{{- end -}}
{{- end -}}

{{- define "view.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "view.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
