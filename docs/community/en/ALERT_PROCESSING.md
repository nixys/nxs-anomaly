# Processing an alert: parsing, enrichment, templates, removal

*Русская версия: [ALERT_PROCESSING.md](../ru/ALERT_PROCESSING.md)*

What happens to an alert between the moment a webhook accepts it and the text a
woken person reads — and where along that path data is added, and where it is
removed.

This is a reference for one end-to-end path. Creating an integration, a route and
a chain is in [CONFIGURATION.md](CONFIGURATION.md); what each entity is, in
[DOMAIN_MODEL.md](DOMAIN_MODEL.md); which data lives where and for how long, in
[DATA_INVENTORY.md](DATA_INVENTORY.md).

## 1. The order of stages

```
HTTP request to /integrations/v1/<source>/{key}
        │
        ▼
1. Source parsing     normalize*Alert() — bring the source format to a common shape
        │
        ▼
2. Route selection    selectRoute() — on the labels of the ORIGINAL alert
        │
        ▼
3. Pipeline           extract / set / rename / remove / gsub / truncate / drop
        │             (enrichment and removal; drop ends processing)
        ▼
4. Field normalisation   title, severity, status, dedupe_key
        │
        ▼
5. Grouping           find the active AlertGroup by (integration_id, dedupe_key)
        │
        ▼
6. Escalation and delivery   render the notification template for the channel
```

The order of stages 2 and 3 is not accidental and is explained in §4: **the route
is chosen before the pipeline**, and everything else sees the enriched alert.

The implementation: `internal/engine/ingest.go`, `ingest_sources.go`,
`routing.go`, `pipeline.go`, `notifications.go`, `engine_helpers.go`.

## 2. Parsing: one shape from many sources

Every ingest endpoint has a normaliser that turns the source's format into a
single internal alert. Downstream, the source is indistinguishable: the pipeline,
the route, grouping and templates all work on the same fields.

| Endpoint | Normaliser | `source` value |
|---|---|---|
| `POST /integrations/v1/webhook/{key}` | none (already the target shape) | `webhook`, or the integration's `source_type` |
| `POST /integrations/v1/alertmanager/{key}` | `normalizeAlertmanagerAlert` | `alertmanager` |
| `POST /integrations/v1/pagerduty/{key}` | `normalizePagerDutyAlert` | `pagerduty` |
| `POST /integrations/v1/victorops/{key}` | `normalizeVictorOpsAlert` | `victorops` |
| `POST /integrations/v1/grafana-alerting/{key}` | `normalizeGrafanaAlertingAlert` | `grafana_alerting` |
| `POST /integrations/v1/opensearch/{key}` | `normalizeOpenSearchAlert` | `opensearch` |
| `POST /integrations/v1/elasticsearch/{key}` | `normalizeElasticsearchAlert` | `elasticsearch` |
| `POST /v2/alert/pool` | `IngestLegacyPool` | `nxs-alert-compat` |

### 2.1. The common shape

The fields everything downstream understands:

| Field | Meaning | If absent |
|---|---|---|
| `title` | the headline | `title` → `labels.alertname` → `labels.summary` → `Incoming alert` |
| `message` | the body | empty |
| `status` | `firing` / `resolved` | `status` → `labels.status` → `firing` |
| `severity` | severity | `severity` → `labels.severity` → `unknown` |
| `labels` | a flat `string → string` map | `{}` |
| `annotations` | a flat map, not used in routing | `{}` |
| `dedupe_key` | the grouping key | computed, see §3 |
| `fingerprint` | the source's fingerprint | empty |
| `starts_at`, `ends_at` | times according to the source | `null` |
| `generator_url` | a link back into the source | `null` |
| `notification_channels` | channels for this alert only | unset |
| `emergency_alert` | an emergency; also set by the label `emergency=true` | `false` |
| `payload` | the whole original request body | — |

A `status` from the set of resolved values closes the group: a recovery event
arrives, the group is resolved by the system actor, and all of its alerts move to
`resolved`. A recovery event with no open group returns
`result: resolved_without_group`, which is not an error.

### 2.2. Alertmanager (v4)

The envelope is split per alert: each element of `alerts[]` becomes its own
alert, but **all of them are accepted in one transaction** under one advisory
lock on the integration. An invalid envelope is rejected whole.

| Internal field | From |
|---|---|
| `title` | `annotations.summary` → `annotations.description` → `labels.alertname` → `receiver` |
| `message` | `annotations.description` → `annotations.message` → `title` |
| `status` | `alert.status` → `envelope.status` → `labels.status` → `firing` |
| `severity` | `labels.severity` → `envelope.status` → `unknown` |
| `dedupe_key` | `fingerprint` → `groupKey` → `alertmanager_group:<groupLabels>` |
| `starts_at` / `ends_at` / `generator_url` | `startsAt` / `endsAt` / `generatorURL` |

The whole envelope — receiver, groupLabels, commonLabels, commonAnnotations,
externalURL, truncatedAlerts — is kept in `payload.alertmanager`.

### 2.3. PagerDuty Events v2

| Internal field | From |
|---|---|
| `title` / `message` | `payload.summary` → `client` |
| `status` | `resolved` when `event_action = resolve`, otherwise `firing` |
| `severity` | `payload.severity`, but anything outside `critical`/`warning`/`info` becomes `warning` |
| `labels` | all of `payload.custom_details` plus `component`, `group`, `class`; `payload.source` becomes the `host` label |
| `dedupe_key`, `fingerprint` | `dedup_key` |

### 2.4. VictorOps / Splunk On-Call

| Internal field | From |
|---|---|
| `title` | `entity_display_name` → `entity_id` |
| `message` | `state_message` |
| `status` | `resolved` when `message_type = RECOVERY` |
| `severity` | `CRITICAL`/`PROBLEM` → `critical`, `WARNING` → `warning`, `INFO` → `info`, everything else → `warning` |
| `labels` | `host_name` → `host`, `monitoring_tool`, `service`, **plus every other string field of the body** except the housekeeping ones |
| `dedupe_key`, `fingerprint` | `entity_id` |
| `starts_at` | `state_start_time` (Unix seconds) → ISO |

`message_type: ACKNOWLEDGEMENT` is handled separately and creates no alert.

### 2.5. Grafana Alerting (webhook v2)

Like Alertmanager, with two differences: `status` also becomes `resolved` when
`envelope.state = ok`, and `title` additionally looks at `envelope.title`.
Grafana's own fields — `ruleUrl`, `dashboardURL`, `panelURL`, `silenceURL`,
`values`, `valueString`, `orgId` — are kept in `payload.grafana_alerting`.

### 2.6. OpenSearch Alerting

The Alerting plugin has no webhook format of its own: a notification channel
posts whatever Mustache template is saved on the trigger, and the default one is
plain text rather than JSON. So the shape below is ours, and it goes into the
channel's message field. A non-JSON body is rejected with `400`.

```json
{
  "status": "firing",
  "monitor": { "id": "{{ctx.monitor._id}}", "name": "{{ctx.monitor.name}}" },
  "trigger": { "id": "{{ctx.trigger._id}}", "name": "{{ctx.trigger.name}}",
               "severity": "{{ctx.trigger.severity}}" },
  "period_start": "{{ctx.periodStart}}",
  "period_end": "{{ctx.periodEnd}}",
  "hits": "{{ctx.results.0.hits.total.value}}",
  "error": "{{ctx.error}}",
  "url": "https://opensearch.example.com/app/alerting#/monitors/{{ctx.monitor._id}}"
}
```

Double braces, not triple: `{{ }}` HTML-escapes, so a quote in a monitor name
arrives as `&quot;` and the JSON stays valid. `{{{ }}}` would substitute the
quote as it is and break the body.

| Internal field | From |
|---|---|
| `title` | `trigger.name` → `monitor.name` → `OpenSearch alert`; a bucket-level monitor appends `(bucket_keys)` |
| `message` | `error`, otherwise the title and the hit count |
| `status` | `resolved` on `COMPLETED` (and on `resolved`/`ok`/`closed`), otherwise `firing` |
| `severity` | `trigger.severity` `1`…`5` → `critical`, `error`, `warning`, `info`, `debug` |
| `labels` | the non-empty ones of `monitor`, `trigger`, `hits`, `bucket_keys` |
| `starts_at`, `ends_at` | `period_start`, `period_end` |
| `dedupe_key` | `monitor.id` + `trigger.id` (+ `bucket_keys`), empty parts dropped; a name stands in for an unfilled id |
| `generator_url` | `url` |

The plugin's severity scale is inverted (1 is the highest) and has exactly the
five levels the on-call queue sorts by, so the translation is one to one.

**Closing a group.** The plugin sends recovery as a separate action: on the same
trigger, add a second action with
`action_execution_policy.actionable_alerts: [COMPLETED]` and the same template
with `"status": "resolved"`.

**Bucket-level monitors.** One trigger fires for several buckets at once. Add an
`alerts` array to the template and the envelope is taken apart the way an
Alertmanager one is — N alerts in one transaction under one advisory lock on the
integration:

```json
{
  "monitor": { "id": "{{ctx.monitor._id}}", "name": "{{ctx.monitor.name}}" },
  "trigger": { "id": "{{ctx.trigger._id}}", "severity": "{{ctx.trigger.severity}}" },
  "alerts": [ {{#ctx.newAlerts}} { "bucket_keys": "{{bucket_keys}}" }, {{/ctx.newAlerts}} ]
}
```

An empty `alerts: []` is a `400`: an envelope with no alerts in it means a broken
template, not an event with nothing to do.

### 2.7. Kibana Rules and Elasticsearch Watcher

Both products are templated by the operator too, so they share one endpoint and
one normaliser; what differs is what they can name an alert by — a rule and its
instance, or a watch.

The Kibana webhook connector:

```json
{
  "status": "{{alert.actionGroup}}",
  "severity": "critical",
  "rule": { "id": "{{rule.id}}", "name": "{{rule.name}}" },
  "alert": { "id": "{{alert.id}}" },
  "message": "{{context.message}}",
  "url": "{{context.viewInAppUrl}}",
  "date": "{{date}}"
}
```

A Watcher `webhook` action:

```json
{
  "watch_id": "{{ctx.watch_id}}",
  "execution_time": "{{ctx.execution_time}}",
  "hits": "{{ctx.payload.hits.total}}",
  "metadata": { "severity": "warning", "team": "db" }
}
```

| Internal field | From |
|---|---|
| `title` | `rule.name` → `watch_id` → `message` → `Elasticsearch alert` |
| `status` | `resolved` on `alert.actionGroup = recovered` (and on `resolved`/`ok`/`closed`), otherwise `firing` |
| `severity` | the template's `severity` → `metadata.severity` → `warning` |
| `labels` | everything in `metadata`, plus the non-empty `rule`, `watch`, `hits` |
| `starts_at` | `date` → `execution_time` |
| `dedupe_key` | `rule.id` (or `rule.name`, or `watch_id`) + `alert.id` |
| `generator_url` | `url` |

An action in the `recovered` group closes the group — add one to the same rule
with the same template. Its dedupe key has to be the one that opened the group,
so do not leave `rule.id` and `alert.id` out of the recovery template.

Neither Kibana rules nor Watcher have a severity of their own, so the template
carries it as a literal. The default is `warning` rather than `unknown`:
`unknown` ranks below every known level and would sink these alerts to the bottom
of the on-call queue.

### 2.8. The legacy NXS pool (`/v2/alert/pool`)

Compatibility with `nxs-alert`. `triggerMessage` and `monitoringURL` are
required. `title` is the first line of `triggerMessage`, truncated to 160 runes.
Severity is `critical` when `isEmergencyAlert: true`, otherwise `warning`.
`alertChannel` is parsed into a channel list; if it contains neither `telegram`
nor `call`, both are added, and `email` is always added.

## 3. The deduplication key

Grouping runs on the pair (`integration_id`, `dedupe_key`) among unresolved
groups. The key comes from the first of:

1. `dedupe_key`, if the source sent one — or a normaliser supplied it from
   `fingerprint`, `dedup_key`, `entity_id` or `groupKey`;
2. otherwise the integration's `group_by` field: `k1=v1|k2=v2` built from the
   listed labels that the alert actually carries;
3. otherwise `title=<the headline>`.

A practical consequence: **the pipeline affects grouping**. A build id cut out of
the title collapses a night of failures into one incident; a label added and
named in `group_by` does the opposite and splits alerts into separate groups.

### 3.1. A repeat firing of an already open group

A source normally keeps re-sending the same alert while the problem lasts:
Alertmanager every `repeat_interval`, the others on their own period. Such a
repeat firing joins the same group and does exactly three things: it is stored
as its own row in `alerts`, it increments `alert_count`, and it refreshes the
group's `last_received_at`, `severity`, `title` and labels.

What it does **not** do:

- **it does not advance the escalation.** The position in the chain is a promise
  about time: `WAIT 10 minutes` means the next step runs ten minutes later, not
  the moment the source repeats the alert. The timer (`next_run_at`) belongs to
  the worker, and a repeat neither consumes nor restarts it;
- **it does not undo an acknowledgement.** An ACK is the on-call engineer saying
  "I know, stop waking me". A source restating the same alert does not overrule
  that, or an acknowledged group would page a person on every repeat.

Escalation starts over only when an alert opens a group — a new one, or one that
came back to `open`.

**The reopen policy.** `NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT=true` turns on the
opposite behaviour: any new alert on an acknowledged group takes it back to
`open`, starts a new episode and **runs the chain from step zero** (not from the
position the ACK stopped it at). The policy is off by default. It makes sense
where a source sends a firing only for a genuinely new event rather than on a
repeat timer — otherwise it becomes a permanent wake-up call.

A separate incident after the group was closed is a different case and always
works: a resolved group is not matched by the (`integration_id`, `dedupe_key`)
lookup, so the next firing opens a new group.

## 4. The pipeline: enrichment and removal

An integration's `pipeline` field is a list of stages that rewrites the alert
after the route has been chosen and before everything else.

### 4.1. Why the route comes before the pipeline

Enrichment is edited often and by different people: a `team` label for a
dashboard, a `namespace` pulled out of the title. If that affected route
selection, every such edit would change who gets woken at night, made by somebody
who was not thinking about escalation. So the route follows from the alert as the
source sent it, and reading an integration's routes gives the whole picture of
where its alerts go.

Everything downstream sees the enriched alert: the deduplication key and
therefore the grouping; the stored alert and its labels; the notification text
and the interface.

The one thing the order does not allow is redirecting an alert to a different
escalation chain by rule. That is the point.

### 4.2. The stages

Each stage is **one action** plus an optional `if`. Two actions in one stage are
rejected when the integration is saved: the order between them would not be
visible in the configuration, and order here is the whole semantics.

| Action | Shape | What it does |
|---|---|---|
| `extract` | `{"from": "<field>", "pattern": "…(?P<name>…)"}` | lifts named regexp groups into labels |
| `set` | `{"<label>": "<value>", …}` | adds or overwrites labels |
| `rename` | `{"<old>": "<new>", …}` | renames a label |
| `remove` | `["<label>", …]` | **deletes labels** |
| `gsub` | `{"field": "<field>", "pattern": "…", "replace": "…"}` | rewrites a field by regexp |
| `truncate` | `{"field": "<field>", "max": N}` | bounds a field's length (N is bytes, cut on a rune boundary) |
| `drop` | `true` | **discards the alert entirely** |

The addressable fields are `title`, `message`, `severity` and `label:<name>`, and
nothing else: a stage cannot reach into the raw `payload` or build a structure
the rest of ingest does not expect.

An `if` is exactly one check: `equals`, `matches` or `exists`.

```json
{
  "pipeline": [
    { "extract": { "from": "title", "pattern": "pod (?P<pod>\\S+) in (?P<namespace>\\S+)" } },
    { "if": { "field": "label:namespace", "matches": "^pay" }, "set": { "team": "payments" } },
    { "remove": ["__name__", "prometheus_replica", "customer_email"] },
    { "gsub": { "field": "title", "pattern": "\\s+run-[0-9a-f]+$", "replace": "" } },
    { "truncate": { "field": "message", "max": 2000 } },
    { "if": { "field": "label:severity", "equals": "info" }, "drop": true }
  ]
}
```

### 4.3. Rules worth knowing in advance

- **`extract` does not overwrite what the source sent.** It adds context; to
  actually replace a label, put a `set` after it, so the replacement reads as a
  replacement.
- **`drop` without an `if` is rejected.** An unconditional drop silently disables
  the integration: it keeps answering `202` and wakes nobody. A dropped alert
  returns `result: dropped_by_pipeline` — a `202`, not an error: the sender
  delivered the alert, and the receiver decided by configuration not to act.
- **A failing stage does not lose the alert.** The stage is skipped and
  `alert_pipeline_stage_skipped` is logged with its index. An undelivered alert
  is worse than an unenriched one: the first is a missed incident, the second is
  cosmetic.
- **An empty value does not blank a field.** `title`, `message` and `severity`
  are written back only if the pipeline left them non-empty.
- **An invalid rule is rejected when the integration is saved** (`400`), not on
  the first real alert.
- **Limits:** up to 32 stages, a regexp up to 512 characters, a field longer than
  16 KB is not processed (the stage is skipped), up to 32 named groups in one
  `extract`. Go's regexps are RE2 and have no catastrophic backtracking, so these
  limits are about latency on the ingest hot path rather than about ReDoS.
- **Compilation cache.** A compiled pipeline is cached by the fingerprint of its
  own definition, so an edit takes effect without a restart, and an unchanged
  integration costs one map lookup per alert.

### 4.4. Testing a rule before a real alert

`POST /api/v1/routes/debug/{key}` runs the pipeline exactly as ingest does and
returns a `pipeline` block:

| Response field | What it shows |
|---|---|
| `labels_before` / `labels_after` | labels before and after the pipeline |
| `labels_added` | what the source did not have |
| `labels_changed` | `{"<label>": {"from": …, "to": …}}` |
| `labels_removed` | what was deleted |
| `title_before` / `title_after` | the headline before and after |

The route in the response is computed from the original labels, exactly as at
ingest, so the preview shows what will happen to a real alert.

## 5. Notification templates

### 5.1. Where they are set

An integration's `templates` is an object `{"<channel>": "<template>"}`. The
`default` key applies when a channel has no template of its own. An empty value
or a missing key renders the built-in format (§5.2).

```bash
curl -X PUT "$API/api/v1/integrations/{id}" -d '{
  "templates": {
    "default": "[{{ .severity }}] {{ .title }}\n{{ .reason }}\nGroup: {{ .group_id }}",
    "telegram": "🔥 {{ .title }}\npod={{ .label_pod }} ns={{ .label_namespace }}\n{{ .reason }}"
  }
}'
```

**Which channels read a template.** Today only `telegram` and `email`; each takes
the key with its own name and falls back to `default`. The other channels ignore
templates, which is worth remembering when writing a `default`:

| Channel | Message text |
|---|---|
| `telegram` | `templates.telegram` → `templates.default` → built-in |
| `email` | `templates.email` → `templates.default` → built-in |
| `slack`, `mattermost` | always the built-in format |
| `chatops` | always the built-in format |
| `log` | always the built-in format (the `notification_log_delivery` line) |
| `webhook` | not text: JSON in the Alertmanager shape |
| `call` | a phrase for speech synthesis: `Alert. <title>. See messages for details.` (`Emergency alert.` when `critical`) |
| `mobile` | a push payload; no template |
| `issue` | its own `subject_template` / `body_template` on the chain step, see §5.6 |

Templates are cached in-process for the reference cache's TTL, which equals the
worker's poll interval. An edit through the API clears the cache in the pod that
served it; other replicas pick it up when the TTL expires — not instantly, but
not only after a restart either.

### 5.2. The built-in format

```
[<severity>] <title>
<reason>
<labels other than alertname and severity, as k=v, k=v>
Alert group: <group_id>
```

The label line is sorted, so the same alert always reads the same way, and cut at
400 characters with an ellipsis: Telegram and SMS have their own limits, and an
Alertmanager alert can carry dozens of labels.

### 5.3. Variables

| Variable | Meaning |
|---|---|
| `title` | the alert's headline (`Alert notification` by default) |
| `severity` | severity (`unknown` by default) |
| `reason` | why this notification: the escalation step, `alert resolved`, and so on |
| `group_id` | the alert group's id |
| `status` | the group's status (`open` by default) |
| `labels` | every label on one line, `k=v, k=v` |
| `label_<name>` | one label's value |
| `user_name`, `user_username` | the recipient |

For `label_<name>` the name is lowercased and anything outside `[a-z0-9_]`
becomes an underscore — `kubernetes.io/name` is available as
`{{ .label_kubernetes_io_name }}`. A form like `{{ .labels.pod }}` **does not
work**: the context is flattened into a map of strings before rendering, so a
label has only its flat key. If a label's key collides with a built-in variable,
the built-in wins.

### 5.4. Syntax

The engine is `text/template`. The forms `{{ .title }}`, `{{ title }}` and
`{{title}}` all work — legacy placeholders are rewritten into the dotted form —
as do the CamelCase aliases `{{ .Title }}`, `{{ .Severity }}`, `{{ .GroupID }}`
and `{{ .UserName }}`, which is how the Terraform provider's examples write them.
The usual constructs are available:
`{{ if .severity }}…{{ else }}…{{ end }}`, `{{ range }}`, `{{ with }}`.

Values are substituted literally: text inside an alert's headline that looks like
a placeholder is not expanded again.

### 5.5. What happens when a template is wrong

Delivery does not fail. The template is rendered by a fallback path — a simple
substitution of the known placeholders — and an unknown placeholder stays in the
text as written. The log carries `notification_template_parse_failed` (a syntax
error) or `notification_template_render_failed` (an unknown key, with the
supported ones listed in the same record). The second is logged once per
template, not once per notification.

### 5.6. Templates for the `CREATE_ISSUE` step

The `CREATE_ISSUE` escalation step has two templates of its own, set on the chain
step rather than on the integration:

| Field | Default |
|---|---|
| `subject_template` | `[{{ severity }}] {{ title }}` |
| `body_template` | `Alert group: {{ group_id }}\n\nTitle: {{ title }}\nSeverity: {{ severity }}` |

The context here is narrower: `title`, `severity`, `group_id`, `status`,
`labels`. There are no recipient variables — a tracker issue has no addressee.

## 6. Removing data

Removal lives at three different levels, and they should not be confused: the
first decides what reaches the database at all, the second what a reader sees,
the third how long it is kept.

### 6.1. At ingest — the pipeline

The only place where data can be **not stored at all**.

- `remove` — drop labels before the write. This is how technical noise goes
  (`__name__`, `prometheus_replica`) and, more importantly, values that should
  never be in an alerting system: customer addresses and phone numbers that ended
  up in monitoring labels.
- `gsub` — cut or mask a fragment inside `title`, `message` or a label when the
  field as a whole is needed.
- `truncate` — bound a field's length.
- `drop` — do not store the alert at all.

One limit worth knowing: the pipeline works on `title`, `message`, `severity` and
labels. **The original request body is stored in the alert's `payload` as it
came** and the pipeline does not edit it. If something has to be removed from
there, either the source must stop sending it, or the group retention horizon
(§6.3) has to be set with that in mind.

### 6.2. On read — redaction

Fields that look like credentials are masked on the API read path (`***` plus the
last four characters): a mobile device's push token and a ChatOps channel's
`webhook_url`. Delivery reads the same rows through the store and sees the real
values — a deliberate split: the delivery path is code, the read path is an
answer to a person. `webhook_url` stays visible to those who may edit the
configuration, or the settings form could not be saved without blanking the
value.

### 6.3. By horizon — retention

The sweep runs every worker cycle, per category, independently. `0` means keep
forever, and that is also the default for new categories, because upgrading a
version must not start deleting data nobody asked to delete.

| Category | Variable | Default | What it removes |
|---|---|---|---|
| `alert_groups` | `NXS_ANOMALY_ALERT_GROUP_TTL_DAYS` | `30` | resolved groups and their alerts, `payload` included |
| `chatops_messages` | `NXS_ANOMALY_CHATOPS_MESSAGES_TTL_DAYS` | `30` | the ChatOps conversation mirror |
| `audit_events` | `NXS_ANOMALY_AUDIT_RETENTION_DAYS` | `0` | the audit trail: actor, IP, action |
| `notifications` | `NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS` | `0` | who was woken, where and when |
| `delivery_attempts` | `NXS_ANOMALY_DELIVERY_ATTEMPT_RETENTION_DAYS` | `0` | delivery attempts: provider answers, error text |
| `web_sessions` | `NXS_ANOMALY_WEB_SESSION_RETENTION_DAYS` | `0` | browser sessions: IP and user agent |

Deletions are visible in the log (`retention_deleted`, at info) and in
`nxs_anomaly_retention_deleted_total{category}`. A category with a horizon set
and a flat counter is a sweep that stopped working. A sweep error is logged at
error level: data that should have gone is still here.

A manual equivalent exists for two of the categories through the API — archiving
resolved groups and ChatOps messages.

### 6.4. On request — export and erasure

Separate administrative operations over one user's data
(`export_personal_data`, `erase_personal_data`), both audited — the erasure
included. The record of a deletion names no personal value: an id, the actor, the
time and the counts. The details and the full catalogue of what lives where are
in [DATA_INVENTORY.md](DATA_INVENTORY.md).

## 7. Observing ingest

| Signal | What it says |
|---|---|
| `result` in the ingest response | `ingested`, `resolved`, `resolved_without_group`, `dropped_by_pipeline` |
| `alert_pipeline_stage_skipped` | a stage did not run; the record carries its index and the integration |
| `alert_pipeline_ignored` | the integration's pipeline does not compile and was ignored whole |
| `notification_template_parse_failed` | the template did not parse; the fallback render was used |
| `notification_template_render_failed` | an unknown placeholder; the record lists the supported ones |
| the `ingest.alert` span | ingest tracing; joined to escalation and delivery through the group id (see [TRACING.md](TRACING.md)) |

The group's log records the ingest stage too: `group_created`, `alert_attached`,
`source_resolve`.
