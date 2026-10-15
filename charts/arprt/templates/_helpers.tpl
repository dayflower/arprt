{{- define "arprt.fullname" -}}
{{- printf "%s-arprt" .Release.Name | trunc 58 | trimSuffix "-" -}}
{{- end -}}

{{- define "arprt.selectorLabels" -}}
app.kubernetes.io/name: arprt
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "arprt.labels" -}}
{{ include "arprt.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "arprt.claimName" -}}
{{- default (printf "%s-data" (include "arprt.fullname" .)) .Values.persistence.existingClaim -}}
{{- end -}}

{{- define "arprt.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (required "image.tag is required when image.digest is empty" .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "arprt.validate" -}}
{{- $raw := required "byos.publicBaseURL is required" .Values.byos.publicBaseURL -}}
{{- $url := urlParse $raw -}}
{{- if or (not (has $url.scheme (list "http" "https"))) (empty $url.host) (not (empty $url.userinfo)) (contains "?" $raw) (contains "#" $raw) -}}
{{- fail "byos.publicBaseURL must be absolute HTTP(S) without userinfo, query or fragment" -}}
{{- end -}}
{{- if regexMatch ":[0-9]+$" $url.host -}}
{{- $port := regexFind "[0-9]+$" $url.host | atoi -}}
{{- if or (lt $port 1) (gt $port 65535) -}}
{{- fail "byos.publicBaseURL port must be between 1 and 65535" -}}
{{- end -}}
{{- end -}}
{{- $path := trimSuffix "/" $url.path -}}
{{- if or (not (regexMatch "^(/[A-Za-z0-9._~-]+)*$" $path)) (regexMatch "(^|/)\\.{1,2}(/|$)" $path) (contains "%" $raw) -}}
{{- fail "byos.publicBaseURL must have a canonical, unescaped path" -}}
{{- end -}}
{{- $seen := dict -}}
{{- range .Values.credentials.authSecrets -}}
{{- if hasKey $seen .name -}}
{{- fail (printf "credentials.authSecrets contains duplicate name %s" .name) -}}
{{- end -}}
{{- $_ := set $seen .name true -}}
{{- end -}}
{{- end -}}
