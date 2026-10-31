{{/*
Expand the name of the chart.
*/}}
{{- define "elida.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "elida.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "elida.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "elida.labels" -}}
helm.sh/chart: {{ include "elida.chart" . }}
{{ include "elida.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "elida.selectorLabels" -}}
app.kubernetes.io/name: {{ include "elida.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "elida.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "elida.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Redis host
*/}}
{{- define "elida.redisHost" -}}
{{- if .Values.redis.enabled }}
{{- printf "%s-redis-master" .Release.Name }}
{{- else }}
{{- .Values.externalRedis.host }}
{{- end }}
{{- end }}

{{/*
Redis port
*/}}
{{- define "elida.redisPort" -}}
{{- if .Values.redis.enabled }}
{{- "6379" }}
{{- else }}
{{- .Values.externalRedis.port | default "6379" }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the control API key.
*/}}
{{- define "elida.controlAuthSecretName" -}}
{{- if .Values.elida.controlAuth.existingSecret -}}
{{- .Values.elida.controlAuth.existingSecret -}}
{{- else -}}
{{- printf "%s-control-auth" (include "elida.fullname" .) -}}
{{- end -}}
{{- end }}

{{/*
Key within that Secret.
*/}}
{{- define "elida.controlAuthSecretKey" -}}
{{- default "api-key" .Values.elida.controlAuth.existingSecretKey -}}
{{- end }}

{{/*
The control API key for a chart-managed Secret, in this order: an explicit
apiKey value, the key already stored in the cluster (so `helm upgrade` does not
rotate it), otherwise a freshly generated one. Only called when the chart owns
the Secret.
*/}}
{{- define "elida.controlAuthKey" -}}
{{- if .Values.elida.controlAuth.apiKey -}}
{{- .Values.elida.controlAuth.apiKey -}}
{{- else -}}
{{- $name := printf "%s-control-auth" (include "elida.fullname" .) -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace $name -}}
{{- $stored := "" -}}
{{- if $existing -}}
{{- $stored = index (default dict $existing.data) (include "elida.controlAuthSecretKey" .) | default "" -}}
{{- end -}}
{{- if $stored -}}
{{- $stored | b64dec -}}
{{- else -}}
{{- randAlphaNum 32 -}}
{{- end -}}
{{- end -}}
{{- end }}

{{/*
Refuse to render a configuration the binary will refuse to start:
internal/config/security.go rejects a non-loopback control bind with neither
authentication nor an explicit allow_insecure acknowledgement.
*/}}
{{- define "elida.validateControlAuth" -}}
{{- $listen := .Values.elida.controlListen -}}
{{- $loopback := or (hasPrefix "127." $listen) (hasPrefix "localhost:" $listen) (hasPrefix "[::1]" $listen) -}}
{{- if and (not .Values.elida.controlAuth.enabled) (not .Values.elida.controlAuth.allowInsecure) (not $loopback) -}}
{{- fail (printf "elida.controlAuth.enabled is false and controlListen is %q, which the control API refuses to start on without authentication. Set elida.controlAuth.enabled=true (recommended), or bind elida.controlListen to 127.0.0.1, or set elida.controlAuth.allowInsecure=true to accept an unauthenticated control API reachable from the whole cluster." $listen) -}}
{{- end -}}
{{- end }}
