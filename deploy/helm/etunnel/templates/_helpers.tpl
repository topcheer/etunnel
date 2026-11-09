{{/* Common labels and helpers */}}
{{- define "etunnel.name" -}}
{{ .Chart.Name }}
{{- end -}}

{{- define "etunnel.fullname" -}}
{{ .Release.Name }}-{{ .Chart.Name }}
{{- end -}}

{{- define "etunnel.labels" -}}
app.kubernetes.io/name: {{ include "etunnel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
