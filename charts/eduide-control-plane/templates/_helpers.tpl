{{- define "ecp.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}{{ .Release.Name | trunc 40 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 40 | trimSuffix "-" }}{{ end }}
{{- end }}

{{- define "ecp.labels" -}}
app.kubernetes.io/name: eduide-control-plane
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{/* selector labels: call with (dict "ctx" . "component" "operator") */}}
{{- define "ecp.selector" -}}
app.kubernetes.io/name: eduide-control-plane
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/* image of a binary: call with (dict "ctx" . "name" "operator") */}}
{{- define "ecp.image" -}}
{{ printf "%s/%s:%s" .ctx.Values.image.registry .name (.ctx.Values.image.tag | default .ctx.Chart.AppVersion) }}
{{- end }}

{{- define "ecp.userSaltSecret" -}}
{{ .Values.userSalt.secretName | default (printf "%s-user-salt" (include "ecp.fullname" .)) }}
{{- end }}

{{- define "ecp.podSecurityContext" -}}
runAsNonRoot: true
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "ecp.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop: [ALL]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* config.yaml: the values as given, agent image and salt file filled in */}}
{{- define "ecp.config" -}}
{{- $c := deepCopy .Values.config -}}
{{- if not $c.agent.image }}{{ $_ := set $c.agent "image" (include "ecp.image" (dict "ctx" . "name" "agent")) }}{{ end -}}
{{- $_ := set $c "telemetry" (mergeOverwrite (dict) ($c.telemetry | default dict) (dict "userSaltFile" "/etc/eduide/secrets/user-salt")) -}}
{{- if .Values.poolScaler.enabled -}}
{{- $_ := set $c "admin" (mergeOverwrite (dict "scalerInterval" (.Values.poolScaler.config.interval | default "30s")) ($c.admin | default dict)) -}}
{{- end -}}
{{- toYaml $c -}}
{{- end }}

{{/* parentRef of the chart's HTTPRoutes */}}
{{- define "ecp.parentRef" -}}
- name: {{ .Values.config.gateway.name }}
  {{- with .Values.config.gateway.namespace }}
  namespace: {{ . }}
  {{- end }}
  {{- with .Values.config.gateway.sectionName }}
  sectionName: {{ . }}
  {{- end }}
  {{- with .Values.config.gateway.port }}
  port: {{ . }}
  {{- end }}
{{- end }}

{{/* volumes and mounts shared by the three Go binaries */}}
{{- define "ecp.volumes" -}}
- name: config
  configMap:
    name: {{ include "ecp.fullname" . }}-config
- name: user-salt
  secret:
    secretName: {{ include "ecp.userSaltSecret" . }}
    items:
    - {key: salt, path: user-salt}
- name: tmp
  emptyDir: {}
{{- end }}

{{- define "ecp.mounts" -}}
- {name: config, mountPath: /etc/eduide/config.yaml, subPath: config.yaml, readOnly: true}
- {name: user-salt, mountPath: /etc/eduide/secrets, readOnly: true}
- {name: tmp, mountPath: /tmp}
{{- end }}
