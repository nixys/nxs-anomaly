# Configuration: from an empty installation to working alerting

*Русская версия: [CONFIGURATION.md](../ru/CONFIGURATION.md)*

A practical guide: what to create, and in what order, so that an alert arriving on
a webhook wakes the person on call.

This document describes **how to configure**. What each entity *is*, and the rules
the engine processes it by, are in [DOMAIN_MODEL.md](DOMAIN_MODEL.md). The full
endpoint reference is in [API.md](API.md).

Every example uses an API key; the same actions are available in the web interface
under the corresponding sections.

The same entities can be described declaratively through Terraform — the
`nxs-anomaly-terraform-provider` provider, the `nxs-anomaly-terraform-module`
module and the `nxs-anomaly-terraform` root project (see "Related repositories" in
the [README](../../../README.md)). The provider talks to this same `/api/v1`, so
everything below remains the source of truth about what gets created and in what
order; Terraform only spares you from passing IDs around by hand.

Mixing the two approaches on one installation will not work, and that is now
visible rather than discovered after the fact. An object created by a Terraform
request gets a `provisioned_by: "terraform"` field; after that the API answers
`403` when anybody else changes or deletes it, and the web interface shows a
"Managed by terraform" badge next to the name and greys out its own edit and
delete buttons. The reason is Terraform's contract: an edit made in the UI lives
until the next `apply` and is then silently rolled back, and nothing used to warn
about that.

What is deliberately not blocked: reading; actions on alert groups (`ack`,
`resolve`, `silence`); schedule overrides (an override is "cover for me tonight",
which Terraform does not describe); rotating an integration key — that is a
response to a leak, and Terraform has no action that performs it.

The tool is identified by `User-Agent` (every terraform-plugin-sdk request sends
one) or by an explicit `X-Nxs-Anomaly-Provisioner: <name>` header. The header is
also how you remove an object whose definition is already gone from the code: name
yourself the same provisioner and delete it by hand. It is not a permission: role
and team boundaries are checked as usual, the header only says whose objects may
be touched.

```bash
export API=http://localhost:8080
export KEY=<your NXS_ANOMALY_API_KEY>
alias api='curl -sS -H "X-API-Key: $KEY" -H "Content-Type: application/json"'
```

## The order of configuration

The entities reference one another, and the engine validates the references on
write: you cannot create an integration pointing at a chain that does not exist,
nor a `NOTIFY_USER` step naming an unknown user. So the order is not arbitrary:

```
users ──┬─→ teams ──┬─→ schedules ──┐
        │           │               ├─→ escalation chains ─→ integrations
        └───────────┴───────────────┘
```

The quick answer to "what is still missing" is the **Setup** page in the web
interface (`/setup`): six onboarding steps whose state is **derived from the
data** rather than stored separately, so the checklist cannot diverge from
reality. It reads the same report the API does:

```bash
api "$API/api/v1/readiness"
```

The UI is available in `ru-RU` and `en-US`. The language choice is stored in the
user's profile; dates are rendered in the chosen locale and their IANA timezone.

## 1. Users

```bash
api -X POST "$API/api/v1/users" -d '{
  "name": "Anna Petrova",
  "username": "anna",
  "email": "anna@example.com",
  "telegram_id": "123456789",
  "timezone": "Europe/Moscow",
  "priority": "high",
  "notification_targets": ["telegram", "email"]
}'
```

`notification_targets` is where a notification actually goes. The valid values are
`log`, `webhook`, `telegram`, `email`, `call`, `slack`, `mattermost`. **If no
target is given, the user gets `log`** — a line in the journal and nothing else.
That is a legitimate choice for a technical account and almost certainly a mistake
for a person.

A channel only works if its transport is configured: `telegram` without
`NXS_ANOMALY_TELEGRAM_BOT_TOKEN` is unreachable. Such notifications get the
terminal status `skipped` with the reason `not_configured` — they do not pretend
to have been delivered. To see which channels are genuinely available, use
`GET /api/v1/readiness`.

`priority` (`high`/`medium`/`low`) affects the choice of duty users in the
`NOTIFY_DUTY_USERS` step — see below.

A sign-in password is set separately; the first administrator comes from
`NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` / `_PASSWORD`.

## 2. Teams

```bash
api -X POST "$API/api/v1/teams" -d '{
  "name": "Platform",
  "member_ids": ["usr_...", "usr_..."]
}'
```

Teams are needed for schedules, the `NOTIFY_TEAM` and `NOTIFY_DUTY_USERS` steps,
and ChatOps channels.

## 3. On-call schedules

A schedule answers the question "who is on call right now". The main mechanism is
a **rotation**: an ordered list of participants and a handoff interval.

```bash
api -X POST "$API/api/v1/schedules" -d '{
  "name": "Platform — primary",
  "team_id": "team_...",
  "timezone": "Europe/Moscow",
  "enabled": true,
  "notify_on_shift_change": true,
  "rotation": {
    "enabled": true,
    "start_at": "2026-01-05T10:00:00+03:00",
    "handoff_interval": 1,
    "handoff_unit": "weeks",
    "participant_ids": ["usr_a", "usr_b", "usr_c"]
  }
}'
```

- `handoff_unit` is `hours`, `days` or `weeks`. For `days`/`weeks` the handoff is
  counted by the calendar in the schedule's zone, so a handoff at 10:00 stays at
  10:00 across a daylight-saving change. `hours` is an absolute duration.
- `start_at` is the anchor: every subsequent handoff is counted from it.
- `participant_ids` go round in the order given.

### Restricting the hours

If duty only covers working hours, add a `restriction`. **Outside the window there
is nobody on call at all** — that is a gap in coverage, not "the previous person
stays on":

```json
"restriction": {
  "start": "10:00",
  "end": "19:00",
  "days": ["mon", "tue", "wed", "thu", "fri"]
}
```

An empty `days` means "every day". The times are in the schedule's `timezone`.

### Coverage, and why it matters

A schedule with a hole in it pages nobody exactly when it matters most, and does
so silently. So the engine checks coverage in two places:

```bash
api "$API/api/v1/schedules/coverage"
```

On top of that, **a schedule with a gap cannot be attached to an escalation
chain** — see the `NOTIFY_SCHEDULE` step.

### Overrides

A one-off replacement on top of the rotation — for a holiday, say:

```bash
api -X POST "$API/api/v1/schedules/{id}/override" -d '{
  "user_id": "usr_d",
  "start_at": "2026-03-10T10:00:00+03:00",
  "until": "2026-03-17T10:00:00+03:00",
  "reason": "Anna on holiday"
}'
```

The source priority is: **an active override → the rotation → legacy shifts**. If
overrides overlap, the one created later wins.

### Preview

Before calling a schedule finished, look at how it expands:

```bash
api "$API/api/v1/schedules/{id}/preview"
```

The response shows the intervals four weeks ahead, the gaps, the overlaps and
`coverage_ratio`.

## 4. Escalation chains

A chain is an ordered list of steps. The engine walks them top to bottom until it
hits a `WAIT`/`REPEAT` or reaches the end.

**The step type is set by the `kind` field.** The `type` field is not read — a step
using it silently matches no handler and is skipped.

```bash
api -X POST "$API/api/v1/escalation-chains" -d '{
  "name": "Platform — critical",
  "steps": [
    {"kind": "NOTIFY_SCHEDULE", "schedule_id": "sch_..."},
    {"kind": "WAIT", "delay_minutes": 10},
    {"kind": "NOTIFY_TEAM", "team_id": "team_..."},
    {"kind": "WAIT", "delay_minutes": 15},
    {"kind": "NOTIFY_EMERGENCY", "user_id": "usr_lead"},
    {"kind": "REPEAT", "from_position": 0, "max_repeat_count": 3, "cooldown_minutes": 30}
  ]
}'
```

### Every step type

| `kind` | Fields | What it does |
|---|---|---|
| `WAIT` | `delay_minutes` | A pause before the next step. The group gets a `next_run_at` and the worker returns to it later |
| `NOTIFY_USER` | `user_ids`, `notify_policy` | Pages specific people. `notify_policy` (`default`/`important`) picks the recipient's personal policy |
| `NOTIFY_SCHEDULE` | `schedule_id`, `allow_uncovered` | Pages whoever is on call by the schedule **at the moment the step runs** |
| `NOTIFY_TEAM` | `team_id` | Pages every member of the team |
| `NOTIFY_DUTY_USERS` | `team_id`, `fallback_to_all` | Pages whoever has the `on_duty` flag raised |
| `NOTIFY_EMERGENCY` | `user_id` | The emergency recipient. Without `user_id`, `emergency_user_id` from the integration's policy is used |
| `TRIGGER_WEBHOOK` | `webhook_url` | An outbound HTTP call |
| `CREATE_ISSUE` | `url`, `tracker_type`, `token_env`, `project`, templates | Opens a ticket in a tracker |
| `RESOLVE` | — | Closes the group automatically |
| `REPEAT` | `from_position`, `max_repeat_count`, `cooldown_minutes` | Returns to the step at `from_position` |

### The details people usually trip over

**`NOTIFY_SCHEDULE` requires a covered schedule.** If there is a gap in the next
seven days, creating the chain fails. That is a deliberate gate: a step with a hole
pages nobody while looking as though it ran correctly. It can be bypassed
knowingly, but only explicitly:

```json
{"kind": "NOTIFY_SCHEDULE", "schedule_id": "sch_...", "allow_uncovered": true}
```

**`NOTIFY_DUTY_USERS` selects by the flag, not by the schedule.** It takes the
members of `team_id` (without it, all users) that have `on_duty: true`, and from
those the group with the highest `priority`. If nobody is on duty:

- `fallback_to_all: true` (the default) — every candidate is paged;
- `fallback_to_all: false` — nobody is paged, and `no_duty_users` appears in the
  group timeline.

**`REPEAT` counts repeats per group.** `from_position` is a zero-based step index.
When `max_repeat_count` is exhausted, escalation stops and writes
`escalation_repeat_exhausted`.

**`TRIGGER_WEBHOOK` obeys the SSRF gate.** In the production profile, addresses in
private, loopback and link-local ranges are refused, redirects included — see
[SECURITY_PROFILE.md](SECURITY_PROFILE.md).

**`CREATE_ISSUE`: the token goes through `token_env`.** In the production profile
an inline secret in the `token` field is refused; use a reference to an
environment variable (`"token_env": "NXS_ANOMALY_REDMINE_TOKEN"`).

## 5. Integrations and routing

An integration is an alert intake point. The key (`key`) is generated by the engine
and forms part of the ingest URL.

```bash
api -X POST "$API/api/v1/integrations" -d '{
  "name": "Prometheus — production",
  "type": "alertmanager",
  "team_id": "team_...",
  "default_chain_id": "chn_..."
}'
```

The response contains the `key`. To send alerts:

```bash
curl -X POST "$API/integrations/v1/alertmanager/<key>" \
  -H 'Content-Type: application/json' -d @payload.json
```

The key can be reissued (`POST /api/v1/integrations/{id}/rotate-key`); the old one
stops working immediately. Deleting an integration is a **soft delete**: the row
stays for the sake of alert history but stops accepting traffic and drops out of
lists.

### Routes

If every alert from an integration goes to one chain, `default_chain_id` is
enough — the engine creates the single `all` route itself. When different alerts
should take different paths, set `routes` explicitly:

```bash
api -X PUT "$API/api/v1/integrations/{id}" -d '{
  "routes": [
    {
      "name": "database",
      "match_type": "labels",
      "labels": {"service": "postgres"},
      "escalation_chain_id": "chn_db"
    },
    {
      "name": "by alert name",
      "match_type": "regex",
      "pattern": "^Disk(Space|Inodes)",
      "escalation_chain_id": "chn_infra"
    },
    {
      "name": "everything else",
      "match_type": "all",
      "is_default": true,
      "escalation_chain_id": "chn_default"
    }
  ]
}'
```

- `match_type: labels` — a match on every label listed;
- `match_type: regex` — against the title/labels; the pattern is compiled on write,
  so a syntax error returns a 400 straight away rather than at the first alert;
- `match_type: all` — accepts everything.

**Exactly one route must have `is_default: true`.** Not zero, not two: without a
default route an alert that matched nothing would be left without a chain, and two
defaults make the choice non-deterministic. A request with any other count is
refused.

Routes are checked in order, so it makes sense to keep the default one last.

### Checking routing without sending an alert

```bash
api -X POST "$API/api/v1/routes/debug/<key>" -d '{
  "title": "DiskSpaceLow on db-01",
  "labels": {"service": "postgres", "severity": "critical"}
}'
```

The response shows the route chosen, the chain, and exactly who the first step
would notify — creating nothing along the way.

### Heartbeat: noticing a source that has gone quiet

Every other signal the service produces starts with an alert arriving. That leaves
one failure nobody reports: a dead exporter, a cron that stopped, a network
segment that took Alertmanager with it. **From the outside, silence is
indistinguishable from health**, and the longer it lasts the calmer it looks.

```bash
api -X PUT "$API/api/v1/integrations/<id>" -d '{
  "heartbeat": {"interval_seconds": 300}
}'
```

`interval_seconds` is how long a source may stay silent before that is news.
`grace_seconds` allows for a late tick and defaults to a third of the interval.
The minimum interval is 60 seconds: below that the check would fire on ordinary
scheduling jitter, given that the worker cycle is itself five seconds.

It is enabled **per integration** and off by default: plenty of sources legitimately
stay quiet for weeks, and reporting them would train everyone to ignore this
signal.

When a source goes quiet, the service raises an ordinary alert group labelled
`alertname=SourceSilent` — it goes through routing, the escalation chain and
delivery by exactly the same path as any other alert. A dead-man switch that
reached the on-call person by its own private route would be the one alert nobody
ever tested end to end. When the source comes back, the group closes itself.

It is raised **once per silence**, not every cycle: otherwise the group's alert
count would climb, and that would read as a storm from a source that by definition
is sending nothing. There is also the `nxs_anomaly_sources_silent` metric with a
`SourceSilent` rule — for when nxs-anomaly's own delivery is what broke.

### Planned maintenance

Do not switch off heartbeats and routes before maintenance. Create a window that
explicitly ties the time of the work to the integrations affected:

```bash
api -X POST "$API/api/v1/maintenance-windows" -d '{
  "name": "PostgreSQL upgrade",
  "reason": "minor upgrade",
  "team_id": "team_...",
  "integration_ids": ["int_..."],
  "starts_at": "2026-08-04T01:00:00+07:00",
  "ends_at": "2026-08-04T03:00:00+07:00"
}'
```

During the window incoming alerts are not lost: new groups are recorded as
`silenced` until `ends_at`, and the heartbeat does not create `SourceSilent`. A
group that was already escalating before the work began carries on escalating.

## 5.1. The ingest pipeline: parsing and enriching an alert

The full account of the ingest pipeline — from source normalisers to data removal —
is in [ALERT_PROCESSING.md](ALERT_PROCESSING.md). What follows is only how to
configure it.

An integration's `pipeline` field rewrites an alert **after the route has been
chosen** and before anything else.

The route deliberately stays above the pipeline. Enrichment is edited often and by
different people — a `team` label for a dashboard, a `namespace` pulled out of the
title. If that influenced the choice of route, every such edit would change who
gets woken at night, at the hands of somebody who was not thinking about
escalation. So the route follows from the alert as the source sent it, and reading
an integration's routes gives the complete picture of where its alerts go.

Everything downstream sees the enriched alert — and that is where the value is: the
deduplication key and therefore the grouping; the stored alert and its labels; the
notification text. Cutting a build id out of the title so that nightly failures
collect into one incident works. Adding the `pod` and `namespace` the source did
not send reaches both the message and the UI.

The one thing this order does not allow is redirecting an alert to a different
escalation chain by rule. That is the point.

A pipeline is a list of stages. Each stage is **one action** plus an optional `if`
condition. Two actions in one stage are refused: the order between them would not
be visible in the config, and here the order is the entire semantics.

| Action | What it does |
|---|---|
| `extract` | pull a regex's named groups out of a field into labels (grok-like) |
| `set` | add or overwrite labels |
| `rename` | rename a label |
| `remove` | delete labels |
| `gsub` | rewrite a field with a regex |
| `truncate` | bound a field's length |
| `drop` | discard the alert entirely |

The addressable fields are `title`, `message`, `severity` and `label:<name>`.

```json
{
  "name": "prometheus-prod",
  "pipeline": [
    { "extract": { "from": "title", "pattern": "pod (?P<pod>\\S+) in (?P<namespace>\\S+)" } },
    { "if": { "field": "label:namespace", "matches": "^pay" }, "set": { "team": "payments" } },
    { "gsub": { "field": "title", "pattern": "\\s+run-[0-9a-f]+$", "replace": "" } },
    { "truncate": { "field": "title", "max": 200 } },
    { "if": { "field": "label:severity", "equals": "info" }, "drop": true }
  ]
}
```

An `if` condition takes exactly one test: `equals`, `matches` or `exists`.

### What to know before switching this on

- **`extract` does not overwrite what the source sent.** It adds context; to
  actually replace a label, follow it with a `set` — that way the replacement is
  visible as a replacement.
- **`drop` without an `if` is refused.** An unconditional discard silently switches
  the integration off: it goes on answering `202` while waking nobody.
- **A stage failing does not drop the alert.** The stage is skipped and an
  `alert_pipeline_stage_skipped` warning is logged with the stage number. An
  undelivered alert is worse than an unenriched one: the first is a missed
  incident, the second is cosmetic.
- **The limits.** Up to 32 stages, a regex of up to 512 characters, a field longer
  than 16 KB is not processed (the stage is skipped), up to 32 groups in one
  `extract`. Go's regexes are RE2 and have no catastrophic backtracking, so these
  limits are about latency on the hot path, not about ReDoS.
- **A faulty rule is refused when the integration is saved**, not at the first
  alert.

### Testing a rule before a real alert

`POST /api/v1/routes/debug/{key}` runs the pipeline exactly as ingest does and
returns a `pipeline` block with `labels_before` / `labels_after`, `labels_added`,
`labels_changed`, `labels_removed` and `title_before` / `title_after`. A dropped
alert comes back as `result: dropped_by_pipeline`. The route in the response is
computed from the original labels, as it is during ingest, so the preview shows
exactly what will happen to a real alert.

## 6. An integration's notification policy

`notification_policy` governs grouping and escalation by volume:

```bash
api -X PUT "$API/api/v1/integrations/{id}" -d '{
  "notification_policy": {
    "channels": ["telegram", "email"],
    "batch_timeout_seconds": 60,
    "batch_deadline_seconds": 300,
    "epic_threshold_count": 20,
    "epic_threshold_seconds": 600,
    "epic_user_id": "usr_lead",
    "emergency_user_id": "usr_oncall_lead"
  }
}'
```

- `batch_timeout_seconds` is a quiet window: notifications accumulate until that
  many seconds pass with no new ones. `batch_deadline_seconds` is the hard limit
  after which the batch goes out regardless. Both at zero means batching is off and
  every notification leaves immediately.
- `epic_threshold_count` / `epic_threshold_seconds` — if that many alerts arrive
  within the window, `epic_user_id` is notified as well. This guards against "a
  storm is under way and the person on call cannot keep up".
- `emergency_user_id` is the recipient of a `NOTIFY_EMERGENCY` step that did not
  set a `user_id` of its own.

## 7. Personal notification policies

By default all of a user's `notification_targets` fire at once. A personal policy
turns that into a ladder with waits — the notification walks the channels in turn
until the person acknowledges the alert:

```bash
api -X PUT "$API/api/v1/users/{id}" -d '{
  "notification_policies": {
    "default": [
      {"channel": "telegram", "wait_minutes": 0},
      {"channel": "email",    "wait_minutes": 5},
      {"channel": "call",     "wait_minutes": 10}
    ],
    "important": [
      {"channel": "call",     "wait_minutes": 0},
      {"channel": "telegram", "wait_minutes": 2}
    ]
  }
}'
```

- This is **opt-in**: without `notification_policies` the behaviour does not
  change.
- Steps with `wait_minutes: 0` go out back-to-back in one cycle.
- The run **stops the moment the group is acknowledged or resolved** — that is the
  whole point of a fallback.
- Which policy applies is decided by the step:
  `{"kind": "NOTIFY_USER", "notify_policy": "important"}`. An empty `important`
  falls back to `default`.

## 8. ChatOps

A channel is a **shared chat**: the address alerting goes to, and the
`chat id → channel` binding by which commands typed there find their owning team.
For the bot's one-to-one correspondence with a person no channel is needed and none
is created: personal notifications go by the user's `telegram_id`, and the buttons
under them run as that person (see [the Telegram bot
section](#adding-a-telegram-bot)).

```bash
api -X POST "$API/api/v1/chatops/channels" -d '{
  "name": "#alerts-platform",
  "platform": "slack",
  "team_id": "team_...",
  "webhook_url": "https://hooks.slack.com/services/...",
  "external_id": "C01234567",
  "notifications_enabled": true,
  "commands_enabled": true
}'
```

Only `platform` and `name` are required. The rest decides what the channel can
actually do:

- `webhook_url` — outbound messages. **Without it the channel exists only inside
  the service**: nothing can be sent to it, and notifications for it get the status
  `skipped` rather than "delivered".
- `external_id` — the channel's identifier on the platform's side (a Slack channel
  id, a Telegram chat id). Incoming slash commands arrive with that rather than the
  internal `id`, so without it commands from this channel will not match.

For inbound commands (acknowledging an alert straight from the chat) configure a
signature: `NXS_ANOMALY_SLACK_SIGNING_SECRET` or
`NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET`. Without the secret the corresponding
endpoint answers `501` — unsigned commands are not accepted.

### Adding a Telegram bot

There are three scenarios; they can be enabled separately and each needs something
different.

| What you want | The bot token (step 2) | The secret + `setWebhook` (steps 4–5) | A ChatOps channel (step 6) |
|---|---|---|---|
| **Personal notifications** — the service writes to a person by their `telegram_id` | yes | no | no |
| **Buttons under a personal notification** — `Acknowledge`, `Resolve`, "I am on duty" | yes | yes | **no** |
| **A ChatOps bot in a shared chat** — alerting into a group and commands from it | yes | yes | yes |

The middle row is the one people most often trip over. The button arrives together
with the personal notification and needs only the webhook: the press comes from a
direct message, a direct message is nobody's ChatOps channel, and the command runs
as the user whose `telegram_id` matched the sender, within their role and teams. A
channel (step 6) is needed precisely for a **shared** chat: it gives the chat an
address to send to (`webhook_url`), the `chat id → channel` mapping for commands
typed there, and the owning team (`team_id`) that bounds what is visible from that
chat.

**1. Create the bot and get a token.**

In Telegram, message [@BotFather](https://t.me/BotFather):

```
/newbot
```

Give it a name and a username (which must end in `bot`). BotFather returns a token
of the form `123456789:AAExampleTokenDoNotUseInProd`.

**2. Put the token into the installation's secret.**

The secret key matches the environment variable name — `envFrom` mounts the whole
secret:

```bash
kubectl create secret generic nxs-anomaly-env \
  --from-literal=NXS_ANOMALY_DB_DSN=... \
  --from-literal=NXS_ANOMALY_TELEGRAM_BOT_TOKEN='123456789:AAExampleTokenDoNotUseInProd'
```

(if the secret already exists — `kubectl edit secret`, or
`--dry-run=client -o yaml | kubectl apply -f -`; do not recreate an existing object
from scratch). In a dev installation with `inlineSecret`, the same variable goes in
`values.yaml`.

That step alone is enough for **personal notifications**: give the user a
`telegram_id` and add `telegram` to their `notification_targets` (see [section
1](#1-users)) — delivery goes through the Bot API's `sendMessage` with no webhook
setup at all.

The `Acknowledge` / `Resolve` buttons under such a notification are the separate
row in the table above: they need steps 4–5 (without a webhook there is nowhere for
Telegram to report a press), but not step 6.

**3. For a shared chat — find the chat id.** (Not needed for personal
notifications and the buttons under them: the service writes to a direct message by
the user's `telegram_id`.)

- A direct message: send the bot anything and open
  `https://api.telegram.org/bot<TOKEN>/getUpdates` — `chat.id` is in the response.
- A group or channel: add the bot as a member, send a message there and look at the
  same `getUpdates` — a group id is negative (`-100...`).

**4. Generate a secret and enable inbound commands.**

```bash
openssl rand -hex 32
```

The value goes into `NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET`, the same way as the
token in step 2. Without that variable the `/integrations/v1/chatops/telegram`
endpoint answers `501` and accepts no unsigned updates.

**5. Register the webhook with Telegram.**

The service's public URL must be HTTPS. Call `setWebhook` with the same secret:

```bash
curl -X POST "https://api.telegram.org/bot<TOKEN>/setWebhook" \
  -d "url=https://<your-host>/integrations/v1/chatops/telegram" \
  -d "secret_token=<the value of NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET>"
```

To check that Telegram sees the webhook and is not accumulating delivery errors:

```bash
curl "https://api.telegram.org/bot<TOKEN>/getWebhookInfo"
```

**6. Create a ChatOps channel — for a shared chat only.** For buttons under
personal notifications this step is skipped: everything already works after step 5.

```bash
api -X POST "$API/api/v1/chatops/channels" -d '{
  "name": "On call — platform",
  "platform": "telegram",
  "team_id": "team_...",
  "external_id": "-100500",
  "notifications_enabled": true,
  "commands_enabled": true
}'
```

`external_id` is the chat id from step 3. For individual people in a shared chat to
be identified by name in the audit trail and in `ack`/`resolve` commands, their
users must also have a `telegram_id` set (step 1 at the start of this section) —
otherwise the command runs as the shared service principal `chatops:telegram` with
the responder role.

To check the whole thing: send `/status` to the bot in the configured chat. The
buttons are checked separately and without a channel — wait for the next alert in
your direct messages and press `Acknowledge`; the group should move to
`acknowledged`, and the person acknowledging in the audit trail should be a human,
not the `chatops:telegram` service principal.

## 9. A proxy for outbound delivery

Needed wherever a provider is not directly reachable: a messenger blocked on the
network, or a cluster whose only route out is a single audited hop. Without a
proxy such a notification fails while establishing the connection, goes into retry
and ends up in the dead letter queue — meaning the alert simply does not arrive.

It is configured through environment variables, like the other delivery
credentials:

| Variable | What it sets |
|---|---|
| `NXS_ANOMALY_DELIVERY_PROXY_URL` | The default proxy for every delivery channel |
| `NXS_ANOMALY_DELIVERY_PROXY_<CHANNEL>_URL` | An override for one channel; `direct` means do not proxy it |
| `NXS_ANOMALY_DELIVERY_NO_PROXY` | Hosts always reached directly |

The schemes are `http`, `https`, `socks5`, `socks5h` and `tcp` (a transparent
relay, HAProxy `mode tcp` — which requires a port). The typical "only the
messengers are blocked" configuration:

```bash
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL='socks5://tunnel.internal:1080'
NXS_ANOMALY_DELIVERY_PROXY_SLACK_URL='socks5://tunnel.internal:1080'
NXS_ANOMALY_DELIVERY_NO_PROXY='receiver.internal,.corp.example'
```

Three things worth knowing before they surprise you:

- **`email` and `call` are not HTTP** — only `socks5://` and `tcp://` carry them,
  and an HTTP proxy is ignored for them;
- **a `tcp://` relay is blind to the destination** — it sends everything to its one
  backend, so it suits channels with a fixed provider host, and for `webhook`,
  `issue` and `chatops` a warning is logged at startup;
- **a mistake in a proxy URL does not turn into "we will send directly"** — the
  channel fails with a clear reason, and `/api/v1/readiness` counts it as a channel
  with no transport;
- **the SSRF gate changes shape for a proxied channel** — the destination is no
  longer checked by IP, because the proxy resolves it.

The whole story — the channels, the interaction with the gate, Helm, diagnostics
and common mistakes — is in its own document, [PROXY.md](PROXY.md).

## 10. Verification

Three levels, from the cheap to the real.

**Installation readiness** — ten checks: the database, the worker, integrations,
routing, channel and duty-user availability, schedule coverage, the team boundary,
retention and backup freshness:

```bash
api "$API/api/v1/readiness"
```

A check that **could not run counts as a blocker, not as a pass**.

**Routing** — `POST /api/v1/routes/debug/<key>`, above.

**A real alert** — the only way to be sure of delivery:

```bash
curl -X POST "$API/integrations/v1/webhook/<key>" \
  -H 'Content-Type: application/json' \
  -d '{"title":"Connectivity check","severity":"critical","status":"firing",
       "labels":{"service":"postgres"}}'
```

Then look at the group's timeline — it shows step by step who was notified and what
the provider answered:

```bash
api "$API/api/v1/alert-groups"
api "$API/api/v1/alert-groups/{id}/timeline"
api "$API/api/v1/delivery-attempts?alert_group_id={id}"
```

The first notification does not go out instantly: delivery is deliberately outside
the ingest transaction, so a delay of up to one worker cycle (`--poll-interval`,
5 seconds by default) is normal rather than a fault.

## Common mistakes

| Symptom | Cause |
|---|---|
| `integration must contain exactly one default route` | `routes` has zero, or more than one, `is_default` |
| Creating a chain fails on `NOTIFY_SCHEDULE` | The schedule has a gap in the next 7 days. Close the hole, or set `allow_uncovered: true` knowingly |
| A step "runs" but notifies nobody | The step type was given as `type` instead of `kind` |
| Notifications with status `skipped` / `not_configured` | The channel is not configured in the environment (no bot token, no SMTP, and so on) |
| `channel_blocked_by_policy` | The channel is forbidden through `NXS_ANOMALY_BLOCKED_CHANNELS` |
| `destination_not_allowlisted` | The destination is not in `NXS_ANOMALY_EGRESS_ALLOWLIST` |
| A notification "went" but the person received nothing | The user has empty `notification_targets` → the `log` fallback |
| `NOTIFY_DUTY_USERS` pages everybody | Nobody has `on_duty: true`, so `fallback_to_all` kicked in |
| `notify_skipped_unknown_users` in the timeline | The schedule references a deleted user |
| `TRIGGER_WEBHOOK` does not pass in production | The SSRF gate: the address is private, loopback or link-local |
| `delivery proxy ... is misconfigured` | A typo in `NXS_ANOMALY_DELIVERY_PROXY_*_URL`; the service deliberately does not fall back to sending directly |
| A 400 about `env:` when saving a secret | The production profile forbids inline secrets; an `env:VARIABLE_NAME` reference is required |

## What next

- [DOMAIN_MODEL.md](DOMAIN_MODEL.md) — the data model and the engine's rules
- [API.md](API.md) — the full endpoint reference
- [SECURITY_PROFILE.md](SECURITY_PROFILE.md) — the production profile and what it tightens
- [ALERTING_RULES.md](ALERTING_RULES.md) — monitoring nxs-anomaly itself
- [DATA_INVENTORY.md](DATA_INVENTORY.md) — what personal data is held where, retention horizons, export and erasure on request
