{{/*
Base chart name.
*/}}
{{- define "gophprofile.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{/*
Fully qualified release name, e.g. "gophprofile" or "myrelease-gophprofile".
*/}}
{{- define "gophprofile.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Common labels applied to every resource.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels for a given component ("server", "worker", "postgres",
"rabbitmq", "minio"). Selector labels must never change across upgrades, and
must never be combined with gophprofile.labels/componentLabels in the same
map: both already include name/instance and duplicate map keys are invalid
YAML.
*/}}
{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ $.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Common labels plus an app.kubernetes.io/component label for a given
component. Use this (not gophprofile.labels + gophprofile.selectorLabels
together) for every resource's metadata.labels.
*/}}
{{- define "gophprofile.componentLabels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Name of the ConfigMap holding shared, non-secret configuration.
*/}}
{{- define "gophprofile.configMapName" -}}
{{ include "gophprofile.fullname" . }}-config
{{- end -}}

{{/*
Name of the Secret holding credentials, honoring secret.existingSecret.
*/}}
{{- define "gophprofile.secretName" -}}
{{- if .Values.secret.existingSecret -}}
{{ .Values.secret.existingSecret }}
{{- else -}}
{{ include "gophprofile.fullname" . }}-secret
{{- end -}}
{{- end -}}

{{/*
Name of the ServiceAccount shared by the server and worker pods.
*/}}
{{- define "gophprofile.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{ include "gophprofile.fullname" . }}
{{- else -}}
default
{{- end -}}
{{- end -}}
