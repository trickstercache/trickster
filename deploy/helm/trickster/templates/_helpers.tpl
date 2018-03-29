{{- define "trickster.image" -}}
{{- $version := default .Chart.AppVersion .Values.versionOverride -}}
{{- printf "%s/trickster:%s" .Values.registry $version | quote -}}
{{- end -}}

{{- define "trickster.version" -}}
{{- default .Chart.AppVersion .Values.versionOverride | quote -}}
{{- end -}}