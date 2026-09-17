{{/*
Expand the name of the chart.
*/}}
{{- define "nxs-anomaly.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name.
*/}}
{{- define "nxs-anomaly.fullname" -}}
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

{{- define "nxs-anomaly.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every object.
*/}}
{{- define "nxs-anomaly.labels" -}}
helm.sh/chart: {{ include "nxs-anomaly.chart" . }}
{{ include "nxs-anomaly.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: nxs-anomaly
{{- end -}}

{{/*
Selector labels (stable across upgrades — never add version here).
*/}}
{{- define "nxs-anomaly.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nxs-anomaly.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Per-component labels. Call with (dict "ctx" . "component" "api").
*/}}
{{- define "nxs-anomaly.componentLabels" -}}
{{ include "nxs-anomaly.labels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Pod labels for the bundled datastores. Deliberately without helm.sh/chart and
app.kubernetes.io/version: a StatefulSet pod template that carries them gets a
new controller revision on every chart bump, so an upgrade that changes nothing
about the database still restarts the pod holding the data. Stateless workloads
keep the full set — they roll on a new image anyway.
*/}}
{{- define "nxs-anomaly.datastorePodLabels" -}}
{{ include "nxs-anomaly.componentSelectorLabels" . }}
app.kubernetes.io/managed-by: {{ .ctx.Release.Service }}
app.kubernetes.io/part-of: nxs-anomaly
{{- end -}}

{{- define "nxs-anomaly.componentSelectorLabels" -}}
{{ include "nxs-anomaly.selectorLabels" .ctx }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Component object name, e.g. "<release>-nxs-anomaly-api".
*/}}
{{- define "nxs-anomaly.componentName" -}}
{{- printf "%s-%s" (include "nxs-anomaly.fullname" .ctx) .component | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
ServiceAccount name.
*/}}
{{- define "nxs-anomaly.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "nxs-anomaly.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Name of the Secret that carries the application's environment (DB DSN, provider
credentials, …). It is either an operator-managed secret named here, or the
secret this chart materialises (inline / ExternalSecret / VaultStaticSecret),
which all use the chart fullname.
*/}}
{{- define "nxs-anomaly.secretName" -}}
{{- if .Values.existingSecret.enabled -}}
{{- required "existingSecret.enabled is true but existingSecret.name is empty" .Values.existingSecret.name -}}
{{- else -}}
{{- include "nxs-anomaly.fullname" . -}}
{{- end -}}
{{- end -}}

{{/*
The VaultAuth the VaultStaticSecret authenticates through. When the chart renders
its own VaultAuth it must also be the one referenced, otherwise the release would
create an object nothing uses while VSO keeps falling back to its default. Empty
means no vaultAuthRef at all, which is that default.
*/}}
{{- define "nxs-anomaly.vaultAuthRef" -}}
{{- if .Values.vaultSecretOperator.vaultAuth.create -}}
{{- include "nxs-anomaly.fullname" . -}}
{{- else -}}
{{- .Values.vaultSecretOperator.vaultAuthRef -}}
{{- end -}}
{{- end -}}

{{/*
Backend (API/worker) container image.
*/}}
{{- define "nxs-anomaly.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Frontend (nginx) container image.
*/}}
{{- define "nxs-anomaly.frontendImage" -}}
{{- $tag := default .Chart.AppVersion .Values.frontend.image.tag -}}
{{- printf "%s:%s" .Values.frontend.image.repository $tag -}}
{{- end -}}

{{/*
In-cluster service hostnames for the bundled databases.
*/}}
{{- define "nxs-anomaly.postgresqlService" -}}
{{- printf "%s-postgresql" (include "nxs-anomaly.fullname" .) -}}
{{- end -}}

{{/*
The PostgreSQL DSN the application connects with. When the bundled PostgreSQL is
enabled it points at the in-cluster service; otherwise it is assembled from
externalPostgres (password comes from the mounted secret, so it is NOT put here —
this DSN is only rendered into the inline secret for the dev/bundled path).
*/}}
{{- define "nxs-anomaly.dbDsn" -}}
{{- if .Values.postgresql.enabled -}}
{{- $a := .Values.postgresql.auth -}}
{{- printf "postgres://%s:%s@%s:5432/%s?sslmode=disable" $a.username $a.password (include "nxs-anomaly.postgresqlService" .) $a.database -}}
{{- else if .Values.externalPostgres.dsn -}}
{{- .Values.externalPostgres.dsn -}}
{{- else -}}
{{- $e := .Values.externalPostgres -}}
{{- printf "postgres://%s:%s@%s:%v/%s?sslmode=%s" $e.username $e.password $e.host (int $e.port) $e.database $e.sslmode -}}
{{- end -}}
{{- end -}}

{{/*
Render topologySpreadConstraints, injecting each constraint's labelSelector from
the component so callers only specify maxSkew/topologyKey/whenUnsatisfiable and a
"labelSelectorComponent". Call with (dict "ctx" . "component" "api" "constraints" $list).
*/}}
{{- define "nxs-anomaly.topologySpread" -}}
{{- if .constraints }}
topologySpreadConstraints:
{{- range .constraints }}
  - maxSkew: {{ .maxSkew | default 1 }}
    topologyKey: {{ .topologyKey | default "kubernetes.io/hostname" }}
    whenUnsatisfiable: {{ .whenUnsatisfiable | default "ScheduleAnyway" }}
    labelSelector:
      matchLabels:
        {{- include "nxs-anomaly.componentSelectorLabels" (dict "ctx" $.ctx "component" (.labelSelectorComponent | default $.component)) | nindent 8 }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
Preflight: when the production security profile is requested, refuse to render an
unsafe deployment. Bundled databases and inline secrets are dev/kind conveniences;
weakening the SSRF guard or the secure-cookie flag defeats the profile. Failing at
template/install time turns a silent misconfiguration into a clear stop. Values can
set the profile either in .config or .extraConfig.
*/}}
{{- define "nxs-anomaly.preflight" -}}
{{- $profile := default (get .Values.extraConfig "NXS_ANOMALY_PROFILE") (get .Values.config "NXS_ANOMALY_PROFILE") -}}
{{- if eq (lower (toString $profile)) "production" -}}
{{-   if .Values.inlineSecret.enabled -}}
{{-     fail "production profile: inlineSecret.enabled must be false — use existingSecret, externalSecrets or vaultSecretOperator" -}}
{{-   end -}}
{{/*
The bundled datastores this profile refuses. Accumulated rather than written as
one expression so an edition that ships fewer of them drops whole lines.
*/}}
{{-   $bundled := list .Values.postgresql -}}
{{-   range $ds := $bundled -}}
{{-     if $ds.enabled -}}
{{-       fail "production profile: bundled datastores are dev-only — set them .enabled=false and point external* at managed services" -}}
{{-     end -}}
{{-   end -}}
{{-   $merged := merge (deepCopy .Values.extraConfig) .Values.config -}}
{{-   if eq (toString (get $merged "NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS")) "false" -}}
{{-     fail "production profile: NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS must not be disabled (SSRF guard)" -}}
{{-   end -}}
{{-   if eq (toString (get $merged "NXS_ANOMALY_SESSION_COOKIE_SECURE")) "false" -}}
{{-     fail "production profile: NXS_ANOMALY_SESSION_COOKIE_SECURE must not be disabled" -}}
{{-   end -}}
{{-   if not (has (lower (toString (default (get .Values.extraConfig "NXS_ANOMALY_TEAM_SCOPING") (get .Values.config "NXS_ANOMALY_TEAM_SCOPING")))) (list "true" "false")) -}}
{{-     fail "production profile: NXS_ANOMALY_TEAM_SCOPING must be set explicitly to \"true\" or \"false\" — \"true\" confines each user to their team's objects, \"false\" declares one trust domain where every operator may see and page everything. Leaving it unset silently means the second." -}}
{{-   end -}}
{{-   if and .Values.networkPolicy.enabled .Values.serviceMonitor.enabled (not .Values.networkPolicy.extraIngress) -}}
{{-     fail "production profile: networkPolicy.enabled + serviceMonitor.enabled with no networkPolicy.extraIngress leaves Prometheus unable to reach the API/worker — set extraIngress to your Prometheus namespaceSelector/podSelector" -}}
{{-   end -}}
{{- /*
    Transport security to the database. Only checkable on the values the chart
    can see: in the operator-managed secret modes the DSN (and its sslmode) lives
    in the Secret, so this catches the externalPostgres path and the deployment
    docs carry the rest.
  */ -}}
{{-   if not (has (lower (toString .Values.externalPostgres.sslmode)) (list "require" "verify-ca" "verify-full")) -}}
{{-     fail (printf "production profile: unsafe externalPostgres.sslmode %q — use require, verify-ca or verify-full. \"prefer\"/\"allow\" silently fall back to plaintext when the server declines TLS, which is exactly the case worth failing on." (toString .Values.externalPostgres.sslmode)) -}}
{{-   end -}}
{{- /*
    Replica counts. A single replica of a paging system means every rollout,
    node drain and OOM is an outage of the thing that tells you about outages.
    And a PDB that permits no disruption at all makes the node undrainable,
    which is how a cluster upgrade stalls at 3am.
  */ -}}
{{-   range $c := list "api" "frontend" -}}
{{-     $v := get $.Values $c -}}
{{-     if and (or (ne $c "frontend") $v.enabled) (lt (int $v.replicaCount) 2) -}}
{{-       fail (printf "production profile: %s.replicaCount must be at least 2 — a single replica makes every restart a paging outage" $c) -}}
{{-     end -}}
{{-   end -}}
{{-   range $c := list "api" "worker" "frontend" -}}
{{-     $v := get $.Values $c -}}
{{-     if and (or (ne $c "frontend") $v.enabled) $v.podDisruptionBudget.enabled -}}
{{-       if ge (int $v.podDisruptionBudget.minAvailable) (int $v.replicaCount) -}}
{{-         fail (printf "production profile: %s.podDisruptionBudget.minAvailable (%v) must be below %s.replicaCount (%v), otherwise the PDB allows zero voluntary disruptions and no node carrying this pod can ever be drained" $c $v.podDisruptionBudget.minAvailable $c $v.replicaCount) -}}
{{-       end -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- /*
  Checks that hold on every profile. These describe installs that cannot work at
  all, as opposed to installs that merely are not production-grade — which is why
  they are evaluated after the production block: told about both at once, an
  operator should hear the profile violation first.
*/ -}}
{{- $modes := list -}}
{{- if .Values.existingSecret.enabled }}{{- $modes = append $modes "existingSecret" }}{{- end -}}
{{- if .Values.externalSecrets.enabled }}{{- $modes = append $modes "externalSecrets" }}{{- end -}}
{{- if .Values.vaultSecretOperator.enabled }}{{- $modes = append $modes "vaultSecretOperator" }}{{- end -}}
{{- if .Values.inlineSecret.enabled }}{{- $modes = append $modes "inlineSecret" }}{{- end -}}
{{- if gt (len $modes) 1 -}}
{{-   fail (printf "incompatible secret modes: %s are all enabled — exactly one may provide the application Secret. Three of these four render or claim the same Secret name, so the winner would be decided by apply order rather than by anybody's intent." (join ", " $modes)) -}}
{{- end -}}
{{- if .Values.vaultSecretOperator.vaultAuth.create -}}
{{-   if not .Values.vaultSecretOperator.enabled -}}
{{-     fail "vaultSecretOperator.vaultAuth.create=true with vaultSecretOperator.enabled=false: the release would install a VaultAuth that nothing in it authenticates through" -}}
{{-   end -}}
{{-   if .Values.vaultSecretOperator.vaultAuthRef -}}
{{-     fail "vaultSecretOperator.vaultAuth.create=true and vaultSecretOperator.vaultAuthRef are both set — the chart-managed VaultAuth always wins, so the name in vaultAuthRef would be created-and-ignored. Pick one: create the VaultAuth here, or reference the platform's." -}}
{{-   end -}}
{{- end -}}
{{- if and .Values.vaultSecretOperator.extraSecrets (not .Values.vaultSecretOperator.enabled) -}}
{{-   fail "vaultSecretOperator.extraSecrets is set with vaultSecretOperator.enabled=false: those Secrets would never be created, and whatever references them by name — imagePullSecrets, most likely — would keep the pods from starting" -}}
{{- end -}}
{{- range $c := list "api" "worker" -}}
{{-   if lt (int (get (get $.Values $c) "replicaCount")) 1 -}}
{{-     fail (printf "invalid replica count: %s.replicaCount must be at least 1 — nothing pages anybody at 0" $c) -}}
{{-   end -}}
{{- end -}}
{{- if and .Values.frontend.enabled (lt (int .Values.frontend.replicaCount) 1) -}}
{{-   fail "invalid replica count: frontend.replicaCount must be at least 1, or set frontend.enabled=false to leave the UI out entirely" -}}
{{- end -}}
{{- /*
  The chart only composes the DSN in the inline/dev path; in the three
  operator-managed modes NXS_ANOMALY_DB_DSN comes out of the Secret and the chart
  never sees the host. So the empty-host check applies exactly where an empty host
  would actually be rendered into a connection string.
*/ -}}
{{- if and .Values.inlineSecret.enabled (not (hasKey .Values.inlineSecret.data "NXS_ANOMALY_DB_DSN")) (not .Values.postgresql.enabled) -}}
{{-   if and (not .Values.externalPostgres.dsn) (not .Values.externalPostgres.host) -}}
{{-     fail "empty external database host: postgresql.enabled=false and neither externalPostgres.dsn nor externalPostgres.host is set, so the DSN would render as postgres://user:pass@:5432/db and every pod would CrashLoop" -}}
{{-   end -}}
{{- end -}}
{{- /*
  Tracing. Enabled with no collector is not "tracing off": the SDK is wired up,
  every request pays for span creation, and the spans go nowhere.
*/ -}}
{{- if and .Values.tracing.enabled (not .Values.tracing.endpoint) -}}
{{-   fail "tracing.enabled requires tracing.endpoint (an OTLP/HTTP collector URL)" -}}
{{- end -}}
{{- end -}}

{{/*
Image pull secrets block.
*/}}
{{- define "nxs-anomaly.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 0 }}
{{- end }}
{{- end -}}
