{{/*
Common helpers.
*/}}

{{- define "cfunc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "cfunc.fullname" -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "cfunc.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{- define "cfunc.labels" -}}
app.kubernetes.io/name: {{ include "cfunc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{/*
DSN env reference. Uses Values.state.dsnSecret if provided, otherwise
falls back to literal Values.state.dsn passed via Secret created by
this chart.
*/}}
{{- define "cfunc.dsnEnv" -}}
- name: CFUNC_STATE_DSN
  valueFrom:
    secretKeyRef:
      {{- if .Values.state.dsnSecret.name }}
      name: {{ .Values.state.dsnSecret.name }}
      key: {{ .Values.state.dsnSecret.key }}
      {{- else }}
      name: {{ include "cfunc.fullname" . }}-state
      key: dsn
      {{- end }}
{{- end -}}
