# API reference

*Русская версия: [API.md](../ru/API.md)*

nxs-anomaly exposes two HTTP surfaces:

1. **Alert ingest** — `/integrations/v1/*`, `/v2/alert/pool` (authenticated by the
   integration key in the URL)
2. **The management API** — `/api/v1/*` (authenticated by an API key) — **the native
   contract**

Plus the operational endpoints: `/live`, `/health`, `/ready`, `/metrics`.

> **The machine-readable contract:** the native `/api/v1` surface is formalised in
> [`openapi.json`](../../openapi.json) (OpenAPI 3.1) — authentication schemes, the
> common error model (`{"error": "…"}`), the pagination envelope and field-level
> schemas for the main objects: identity, user, team, schedule, escalation chain,
> integration, alert group, alert, notification and delivery attempt. In those
> schemas `required` lists exactly the keys the engine writes unconditionally, so
> what is optional there really can be absent; all of them allow additional
> properties, because storage is JSONB.
>
> The frontend compiles against types generated from that file
> (`frontend/src/api/schema.d.ts`, `npm run openapi:types`): `src/api/types.ts` and
> `src/api/client.ts` reference the generated schemas rather than redeclaring them,
> so a change to the specification surfaces as a TypeScript error. `npm run
> openapi:check` fails if the committed generated file has drifted from the
> specification.
>
> `internal/server/openapi_contract_test.go` catches divergence in both directions:
> every documented route must dispatch (a panicking handler counts as a failure,
> not a success), every `/api/v1` route literal in the router must match a
> documented path segment by segment, every sub-resource routed by suffix
> (`…/timeline`, `…/duty-on`) must terminate a documented path, and every
> authorisation prefix in `auth.go` must lead somewhere documented. This prose
> reference remains the human-readable presentation of the same thing.

---

## Authentication

The management API (`/api/v1/*`) is closed by default. People use a web session
after signing in with a password; automation uses a scoped API key. With no
credentials configured, requests are rejected; open dev mode is enabled only
explicitly, with `NXS_ANOMALY_ALLOW_ANONYMOUS=true`.

An API key travels in either of two headers:

```
X-API-Key: <token>
Authorization: Bearer <token>
```

### User sessions

The current principal and its presentation settings:

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/auth/me` | The current principal, its permissions, teams, `locale` and `timezone`. For a service principal the last two are empty. |
| PUT | `/api/v1/auth/preferences` | Change your own `locale` and/or `timezone`. Available to any user role; this endpoint cannot change permissions or any other user field. |

Supported locales are `ru-RU`, `en-US` and the empty string ("follow the
browser"). The timezone is an IANA name (`Europe/Moscow`, `Asia/Novosibirsk`,
`UTC`); an unknown name returns 400. For example:

```bash
curl -X PUT "$API/api/v1/auth/preferences" \
  -H 'Content-Type: application/json' \
  -d '{"locale":"en-US","timezone":"Europe/Berlin"}'
```

People sign in as themselves; API keys are for automation. A session is an
HttpOnly cookie with `SameSite=Strict`: page scripts cannot read it, and it is
never returned in a response body.

| Method | Path | Access | Description |
|---|---|---|---|
| GET | `/api/v1/auth/methods` | unauthenticated | Which sign-in methods this installation offers. |
| POST | `/api/v1/auth/login` | unauthenticated | `{"login": "...", "password": "..."}` → a session cookie. Rate-limited per IP. |
| POST | `/api/v1/auth/logout` | unauthenticated | Revokes the presented session. |
| GET | `/api/v1/auth/me` | any | The current subject, its role and its effective permissions. |
| POST | `/api/v1/auth/password` | any | Change your own password (`current_password`, `new_password`). Signs you out everywhere. |
| GET | `/api/v1/auth/sessions` | any | Your own live sessions; your current one is marked `current`. |
| DELETE | `/api/v1/auth/sessions/{id}` | any | Revoke one of your own sessions. Somebody else's id gives `404`. |
| DELETE | `/api/v1/users/{id}/sessions` | admin | Sign a user out everywhere without touching their password. |
| PUT | `/api/v1/users/{id}/password` | admin | Set another user's password. Signs them out. |
| DELETE | `/api/v1/users/{id}/password` | admin | Remove a user's password. Signs them out. |
| GET | `/api/v1/users/{id}/export` | admin | Everything the installation holds about a person. The password hash and token hashes are never exported; the existence of a password is reported. The audit sections are bounded by `audit_export_limit`, and that number is returned in the response. |
| POST | `/api/v1/users/{id}/erase` | admin | Erase a person's personal data. Credentials, sessions, devices and ChatOps channels are deleted; the user record, delivery addresses and `actor_name`/`request_ip` in the audit trail are pseudonymised; the audit events themselves are left alone. The response is a verification (`verified`, `residue`), not a promise; copies outside the database are listed in `out_of_scope`. Idempotent. |

Signing in requires **both** a password **and** a role. A user without a role is
an entry in a list: they can be paged, but they cannot sign in. Because session
lookup reads the user record on every request, removing a role or deleting a user
takes effect immediately, without waiting for the session to expire.

Passwords are stored as PBKDF2-HMAC-SHA256 (600,000 iterations, a per-user salt);
session tokens as SHA-256, so a database dump contains no usable sessions. The
minimum password length is 12 characters.

| Environment variable | Default | Description |
|---|---|---|
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` | — | The break-glass administrator: created (or promoted) on every start. |
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD` | — | Its password, reset on every start. |
| `NXS_ANOMALY_SESSION_TTL_SECONDS` | `43200` | Absolute session lifetime (12 h). |
| `NXS_ANOMALY_SESSION_COOKIE_SECURE` | `true` | `false` is for local development over plain HTTP only. |
| `NXS_ANOMALY_AUDIT_RETENTION_DAYS` | `0` | Delete audit events older than N days. `0` keeps everything. |
| `NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS` | `0` | Delete finished notifications older than N days. |
| `NXS_ANOMALY_DELIVERY_ATTEMPT_RETENTION_DAYS` | `0` | Delete delivery-attempt records older than N days. |
| `NXS_ANOMALY_WEB_SESSION_RETENTION_DAYS` | `0` | Delete web sessions older than N days. |
| `NXS_ANOMALY_BLOCKED_CHANNELS` | — | Forbidden delivery channels, comma-separated. |
| `NXS_ANOMALY_EGRESS_ALLOWLIST` | — | Permitted outbound delivery destinations: hosts and/or CIDRs. |
| `NXS_ANOMALY_MOBILE_STREAM_INTERVAL_SECONDS` | `10` | How often the phone's event stream re-reads: the latency between an alert and a phone ringing. |
| `NXS_ANOMALY_MOBILE_STREAM_MAX` | `200` | Concurrent event streams one replica serves; beyond it, 503 with Retry-After. |
| `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT` | — | Private destinations the SSRF guard lets through: hosts, `.domain` suffixes and/or CIDRs. Loopback and link-local are never excepted. |

Every event carries its request's `request_id` — the same value is returned in the
`X-Request-ID` header and written to the access log — so "everything one request
did" is one query away: `GET /api/v1/audit?request_id=...`. Events written by the
worker do not carry it: there is no request behind them. The trail is also
browsable by administrators on the `/audit` page.

Audit retention is off by default: the value of the trail is its completeness, so
deleting from it is an explicit decision with a named horizon. When it is on, a
worker pass disables the table's append-only trigger inside a single transaction,
deletes the rows, re-enables the trigger and records how many rows went. This is a
deliberate narrow breach — no request path reaches it, and the service has always
owned its own schema — made because the alternative was unbounded growth with a
manual `ALTER` as the undocumented way to clean up.

The bootstrap variables are also the password-recovery path, which is why they are
applied on every start rather than only against an empty database. Remove them
once the break-glass account is no longer needed.

### Roles

| Role | Can |
|---|---|
| `viewer` | read everything |
| `responder` | + acknowledge / unacknowledge / resolve / reopen / silence |
| `editor` | + create and change configuration (integrations, chains, schedules, teams) |
| `admin` | + access management, the audit trail, forced escalation and route debugging |

The roles are ordered: each includes everything the previous one grants.

### API keys (automation)

| Environment variable | Format | Effect |
|---|---|---|
| `NXS_ANOMALY_API_KEY` | a single token | admin (full access) |
| `NXS_ANOMALY_API_KEYS` | `tok1:admin,tok2:viewer` | a role per token |

An entry in `NXS_ANOMALY_API_KEYS` without `:role` resolves to **`viewer`**, and
the server warns about it at startup (`api_keys_without_a_role`). It used to mean
admin: automation credentials are obliged to state what they may do, and
inheriting the widest role from a default is the direct opposite of that. An
unrecognised role degrades to `viewer` for the same reason: a mistake in
deployment configuration should narrow access, not widen it. The pre-reform name
`readonly` is still accepted and read as `viewer`.

`NXS_ANOMALY_API_KEY` is unaffected and stays admin: the entire point of that
variable is "the single admin key", and that is an explicit scope named in the
name itself.

**With no credentials configured, the management API rejects every request.**
`NXS_ANOMALY_ALLOW_ANONYMOUS=true` restores the old open behaviour for local
development; the server warns about it on every start.

If a request carries both a session cookie and an API key, the session wins — so
that the audit trail names a person rather than a shared key.

---

## Operational endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/live` | Liveness — 200 while the process is serving HTTP; does not touch the database. For a k8s `livenessProbe`. |
| GET | `/health`, `/ready` | Readiness — pings the database. 503 when the database is unreachable. For a `readinessProbe`. |
| GET | `/metrics` | Prometheus metrics. |

The `/health` response:

```json
{
  "status": "ok",
  "db_ok": true,
  "version": "1.2.3",
  "uptime_seconds": 3600,
  "worker_cycles_completed": 720,
  "last_worker_cycle_at": "2026-06-07T12:00:00+00:00"
}
```

When degraded: `status: "degraded"` and `db_ok: false` alongside `db_error`.

---

## Alert ingest

| Method | Path | Source format |
|---|---|---|
| POST | `/integrations/v1/webhook/{key}` | The universal nxs-anomaly alert |
| POST | `/integrations/v1/alertmanager/{key}` | Prometheus Alertmanager v4 |
| POST | `/integrations/v1/pagerduty/{key}` | PagerDuty Events v2 |
| POST | `/integrations/v1/victorops/{key}` | VictorOps / Splunk On-Call |
| POST | `/integrations/v1/grafana-alerting/{key}` | The Grafana Alerting webhook |
| POST | `/integrations/v1/opensearch/{key}` | OpenSearch Alerting, a channel template |
| POST | `/integrations/v1/elasticsearch/{key}` | Kibana Rules, Elasticsearch Watcher |
| POST | `/v2/alert/pool` | The legacy NXS pool (authenticated by the `X-Auth-Key` header) |

`{key}` is the integration's routing key. HMAC verification through
`X-Hub-Signature-256` or `X-Anomaly-Signature` is optional and applies to
`/integrations/v1/webhook/{key}` when the integration has a `webhook_secret`: the
header is the hex HMAC-SHA256 of the request body exactly as sent, with or without
a `sha256=` prefix. The other formats are not signed by their senders and are
authenticated by the key alone. Returns `202 Accepted` (or `200` for victorops
and the legacy pool), `404` for an unknown key, `400` on a validation error, `429`
when the rate limit is exceeded.

---

## The management API (`/api/v1`)

Standard list endpoints accept `?limit=` (default 100, maximum 1000) and
`?offset=`. A list response is `{"items": [...], "total": N, "limit": L,
"offset": O}`.

**Sorting.** `?sort=<column>&order=asc|desc`. By default, lists that are read
chronologically come back newest first: alert groups by `last_received_at`, alerts
by `received_at`, notifications by `created_at`; everything else by `id`. The
column is checked against the collection's own column list (its typed columns plus
`id`, `created_at`, `updated_at`), and an unknown name is a `400` rather than a
silently ignored parameter. The final sort key is always `id`: without that
tie-breaker an `OFFSET` page can show one row twice and omit another. `severity`
sorts by importance rather than alphabetically (`critical` > `error` > `warning` >
`info` > `debug`).

**Provisioned objects.** If an object has a `provisioned_by` (`"terraform"`, say),
its `PUT`/`PATCH`/`DELETE` answer `403` to everyone except a request from the same
provisioner — which means a request whose `User-Agent` contains `terraform`, or
one carrying the header `X-Nxs-Anomaly-Provisioner: <name>`. More in
[CONFIGURATION.md](CONFIGURATION.md).

### Users

| Method | Path |
|---|---|
| GET/POST | `/api/v1/users` |
| GET/PUT/PATCH/DELETE | `/api/v1/users/{id}` |
| POST | `/api/v1/users/{id}/duty-on`, `/duty-off` |
| POST | `/api/v1/users/{id}/test-notification` — deliver one test notification through the real provider and return the verdict (`{status, provider_status, provider_code?, response?, error?, configured}`); the body is `{channel?}`, defaulting to the first step of the user's `default` policy, otherwise `log`. Admin only. |
| POST | `/api/v1/users/migrate-notification-policies` — build a `default` policy from the legacy `notification_targets` for every user that lacks one (idempotent). Admin only. |

**Personal notification policies (BETA-031).** A user may have
`notification_policies: { default?: Step[], important?: Step[] }`, where each
`Step` is `{ channel: telegram|email|webhook|call|log, target?: string,
wait_minutes: int }`. When a policy is set, paging walks the sequence — notify a
channel, wait, move to the next — instead of firing at every target at once, and
stops the moment the group is acknowledged or resolved. A `NOTIFY_USER` escalation
step can pick the policy through `notify_policy: "important"|"default"` (default
`default`). Users without a policy keep the old behaviour (all targets at once).

The practical configuration guide is in [CONFIGURATION.md](CONFIGURATION.md).

### Teams, schedules, escalation chains, integrations

| Method | Path |
|---|---|
| GET/POST | `/api/v1/teams`, `/api/v1/schedules`, `/api/v1/escalation-chains`, `/api/v1/integrations` |
| GET/PUT/DELETE | `…/{id}` |
| GET | `/api/v1/schedules/{id}/on-call?at=<ISO>` — plus `source` and a `next` segment |
| GET | `/api/v1/schedules/{id}/preview?from=<ISO>&to=<ISO>` — intervals, gaps, overlaps, `unknown_users`, coverage (the next 4 weeks by default) |
| GET | `/api/v1/schedules/coverage` — a standing check across visible schedules: which have degraded, which chains page through them, and whether that has been acknowledged. |
| POST | `/api/v1/schedules/{id}/override` (synonym: `/overrides`) |
| PUT/PATCH/DELETE | `/api/v1/schedules/{id}/overrides/{override_id}` |
| POST | `/api/v1/integrations/{id}/rotate-key` |

An integration's `webhook_secret` is never returned: responses carry
`webhook_secret_set` instead (an `env:` reference is shown as is). To change the
secret, send a new value; to remove it, send `null`. `key` and `routing_key` — the
permission to send alerts — are visible to `editor` and `admin` and masked for
other roles.

`DELETE` of a user, team, schedule or escalation chain answers `409` while
paging still depends on it — an escalation chain step, a rotation, a current or
future shift or override, an integration's route or notification policy. The
error names every dependent object; unlink them and delete again. Past shifts and
overrides do not block and are removed with the user.

### Maintenance windows

| Method | Path |
|---|---|
| GET/POST | `/api/v1/maintenance-windows` |
| GET/PUT/DELETE | `/api/v1/maintenance-windows/{id}` |

A window requires `name`, `starts_at`, `ends_at` and at least one integration. The
bounds are RFC 3339, with `starts_at < ends_at`. While a window is active, new
groups from the listed integrations are created `silenced` until it ends: the
alerts are stored, but paging and the `SourceSilent` heartbeat are suppressed. A
group that is already escalating does not fall silent retroactively. Overlapping
windows are allowed; the latest `ends_at` applies.

A window is an interval (`starts_at`, `ends_at`) during which the integrations
named in `integration_ids` count as under planned maintenance: alert groups they
open during that time are recorded in full but immediately moved to `silenced`
until the window ends, so the worker does not escalate them and nobody is woken.
A group that is already escalating is left alone — somebody has already been
raised for it.

Separately, a window disables the dead-man switch for those integrations: a source
switched off for planned work is a silent source, and without a window every
maintenance would raise `SourceSilent` exactly when there is nobody to answer it,
because the people are busy with that same work.

`integration_ids` must name at least one integration. "Empty means all" is
deliberately unsupported: one typo would silence the whole installation, and the
only symptom of that is that nobody gets woken.

A window with unparseable bounds suppresses nothing: a string nobody can read must
not quietly swallow alerting.

`DELETE` on an integration is a **soft delete** (`deleted_at` is set; the
integration drops out of lists and ingest but stays reachable by ID).

### Inbound ChatOps (signed)

| Method | Path | Authentication |
|---|---|---|
| POST | `/integrations/v1/chatops/slack` | The Slack signing secret (`X-Slack-Signature`, v0 HMAC-SHA256 over the raw body; requests older than 5 minutes are rejected) |
| POST | `/integrations/v1/chatops/telegram` | `X-Telegram-Bot-Api-Secret-Token`, compared in constant time |
| POST | `/integrations/v1/chatops/slack/interactive` | The same Slack signature; the body is form-encoded with JSON in the `payload` field |
| POST | `/integrations/v1/chatops/mattermost` | A secret in the button's `context.token`, compared in constant time (Mattermost does not sign its own callbacks) |

Both endpoints are rate-limited per client address, like any other public route.
Slack gets a `{"response_type":"ephemeral","text":…}` reply so that the person who
typed the command sees the result.

Not configured (`NXS_ANOMALY_SLACK_SIGNING_SECRET` /
`NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET`) → `501`; these endpoints never accept an
unsigned command. The channel is identified by `external_id` (Telegram also falls
back to `name` — that is where existing installations keep the chat id).

**A chat with no channel.** Personal notifications arrive in a direct message with
the bot, and the buttons under them come back with a chat id that is nobody's
ChatOps channel. A command from such a chat is executed **if the sender is
identified** by `telegram_id` — as them, under their role and their teams, without
a channel's team boundary, because there is none. An unidentified sender in an
unlinked chat is refused: they would fall back to the platform's service
principal, and allowing that principal to run commands from any chat would hand
the right to acknowledge alerts to anyone who adds the bot anywhere. For a
**shared** chat a channel is still required — it is both the delivery address and
that same team boundary.

**Who runs the command.** The sender's Telegram id is matched against users'
`telegram_id`. A user that is found acts as themselves: their role decides what is
allowed, their teams bound what is visible, and the audit entry names them. A
sender with no known user behind them runs the command as
`chatops:<platform>` with the responder role — a shared on-call chat remains a
working scenario. A known user whose role has been removed gets a `403`: falling
back to the service principal would restore permissions that were deliberately
taken away. Slack does not use identification — the service does not store its
user identifiers.

**What can be touched.** A group is available to a command if both boundaries let
it through: the actor's teams and the team the channel is bound to. The chat is
context: membership in two teams does not make the chat of one an entrance to the
other. Ownership comes from the group's integration; a group whose integration has
disappeared is refused. Integrations without a team stay available to everyone.

**Commands.** `status` (how many are open; through the API the reply also lists the 50
newest as `id`/`title`/`severity`/`status`, with `open_count` and `truncated`), `alerts [page]` (a list with a button
per group), `ack <id>`, `resolve <id>`, `duty on|off`, `duty take [schedule id]
[hours]`, `priority [username] <high|medium|low>`, `oncall <schedule id>`, `help`.
The Telegram reply is returned as a `sendMessage` method call in the webhook
response body — otherwise Telegram ignores it and the typed command answers
nothing. No second call to the Bot API is needed.

**The alert list.** `alerts` returns the open groups available to the caller,
newest first, 5 to a page. The page number rides in `callback_data` (`alerts:2`)
rather than in a server-side session: a list is read for a few seconds, and a
session would outlive the reason it exists. Pressing "Next" redraws the same
message through `editMessageText` instead of sending another copy of the list. A
page beyond the range is clamped to the last one — pages shrink while they are
being read, and a stale press should show the last page, not an error.

**On and off shift (`duty on|off`).** The schedule decides **who should** be on
call; the `on_duty` flag records **who confirmed it**. The command requires a
sender identified by `telegram_id`: the service principal of a shared chat is not
a person, and letting it check in would put the entire chat on duty.

A check-in expires on its own — at the end of the shift the person actually
covers, or after 12 hours if no schedule currently assigns them (a stand-in during
an incident). A fixed timeout would be either longer than the shift — and the flag
would outlive it, with `NOTIFY_DUTY_USERS` still waking whoever last remembered to
type "duty on" — or shorter, and would clear mid-shift. `on_duty` set through the
API or the web interface is untouched by this mechanism: that is a standing
assignment, not a shift confirmation.

**Taking duty in an emergency (`duty take [schedule id] [hours]`).** This writes a
**schedule override**, not a flag. The flag is invisible to the schedule engine,
so switching with it would leave `NOTIFY_SCHEDULE`, the coverage report and the
preview all still naming the person being replaced — two answers diverging exactly
when it matters. The default is 2 hours, the maximum 24: longer than that is a
change to the schedule, and its place is in the schedule where the team can see
it, not in a chat message nobody will scroll back to. If only one schedule is
available it is selected automatically (hunting for an id at night is the last
thing worth doing); if there are several, the bot lists the candidates and asks
for one. A check-in for the same window is set at the same time, so that it
expires together with the override.

**Priority (`priority [username] <high|medium|low>`).** This decides who
`NOTIFY_DUTY_USERS` reaches first among those on duty. Your own is responder
level, somebody else's is editor: arranging yourself in the queue is your own
business, deciding when a colleague's phone rings is not. Both commands obey the
same two boundaries as actions on groups.

The divergence is visible in the metrics: `nxs_anomaly_duty_on_call` and
`nxs_anomaly_duty_without_checkin`. A schedule can be fully covered on paper while
nobody has confirmed the shift — until now that situation was visible nowhere.

**Buttons.** A Telegram alert notification carries inline `Acknowledge` and
`Resolve` buttons with `callback_data` of the form `<action>:<group id>`. A press
arrives at the same endpoint as a `callback_query` and runs down the same path as
a typed command — the boundaries coincide by construction. The endpoint always
answers `200`: a refusal here is final rather than temporary, and a non-2xx would
make Telegram redeliver the press and run the command twice; the person gets the
verdict through `answerCallbackQuery`. If the `callback_data` does not fit
Telegram's 64-byte limit, no buttons are attached — the alert message matters more
than the shortcut attached to it.

After a button is used the message is rewritten: the verdict is appended to the
text and the keyboard is removed. Otherwise an acknowledged alert would go on
offering "Acknowledge" — the pop-up confirmation disappears in seconds, and what
stays on screen is a button implying the work is still waiting. A refused press
changes nothing and leaves the buttons in place.

A shift-start notification carries an "I am on duty" button (`duty:on`) — checking
in with a tap instead of typing a command.

**Buttons on all three platforms.** Slack gets `blocks` with buttons (the plain
`text` is kept alongside — that is what a lock-screen preview will show),
Mattermost gets `attachments` with `actions`. The actions on every platform come
from one table, so a button added on one cannot mean something else on another;
a test enforces that.

A press in Slack **replaces the message** (`replace_original`); in Mattermost it
updates the post and clears `attachments`. A refusal changes nothing and leaves
the buttons, so that the next person reading can act. In Slack and Mattermost the
command runs as the service principal: the service stores no user identifiers for
them, so there is nothing to identify a person by — the same limitation as a typed
command in Slack.

Mattermost buttons require `NXS_ANOMALY_PUBLIC_URL` and
`NXS_ANOMALY_MATTERMOST_ACTION_SECRET`: their callback goes to an absolute address
and carries no signature, so the secret in the button's context is the only thing
distinguishing a real press from an outside request. Without either one, the alert
goes out **without buttons**: a button leading somewhere nobody answers is worse
than no button.

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/users/{id}/test-push` | Sends a real push through the relay and returns the provider's verdict per device; `push_configured: false` if the relay is not configured. Admin only, like the other writes under `/users`. |

Reads mask fields that look like credentials: a device's `push_token` always, a
ChatOps channel's `webhook_url` and the values of the `headers` of a ChatOps
channel or a `TRIGGER_WEBHOOK` step for everyone who may not change
configuration, as is the inline `token` of a `CREATE_ISSUE` step; the header
values and the tracker token a notification carries — for everyone. Delivery
reads the same rows through the store and is unaffected.

### ChatOps and mobile clients

| Method | Path |
|---|---|
| GET/POST | `/api/v1/chatops/channels` |
| GET/PUT/DELETE | `/api/v1/chatops/channels/{id}` |
| GET | `/api/v1/chatops/messages` (paginated) |
| POST | `/api/v1/chatops/commands` |
| GET/POST | `/api/v1/mobile/devices` |
| POST | `/api/v1/mobile/sessions` |
| POST | `/api/v1/mobile/pairing` — a one-time code to sign a phone in |
| POST | `/api/v1/mobile/pairing/redeem` — exchange the code for a session (unauthenticated) |
| DELETE | `/api/v1/mobile/sessions/current` — sign the phone out |
| GET | `/api/v1/mobile/sessions` — the caller's own phones |
| DELETE | `/api/v1/mobile/sessions/{id}` — sign one of the caller's phones out (a lost one, say) |
| GET | `/api/v1/mobile/events` — the alert groups that concern the caller, as a live stream |
| GET | `/api/v1/mobile/dashboard` (a mobile session) — the groups that concern the caller as list rows, without `logs` and `alert_ids` (read the group for those) |
| POST | `/api/v1/mobile/alert-groups/{id}/acknowledge`, `/resolve` (a mobile session) |

**The event stream** (`GET /api/v1/mobile/events`) is how a phone hears about an
alert in seconds without a push service. It is Server-Sent Events: the app keeps
it open, and on every tick (`NXS_ANOMALY_MOBILE_STREAM_INTERVAL_SECONDS`, 10 by
default) the server sends the whole set of groups that concern this person plus
which of them are `added` or `changed` since the previous tick. The first tick is
a `baseline` and announces nothing, so a phone reconnecting after a dead network
does not ring for what it already knows. A failed read sends an `error` event and
keeps the stream open — a restarting database must not send every phone into a
reconnect loop. SSE comments every 20 seconds keep proxies from closing an idle
connection, and the response carries `X-Accel-Buffering: no` because nginx would
otherwise hold events until its buffer filled. `NXS_ANOMALY_MOBILE_STREAM_MAX`
caps how many streams one replica serves. An API key gets 400: there is no
"concerns me" for one. The first event, `hello`, carries `interval_seconds` and
`can_respond` — whether this person may acknowledge and resolve, so a phone
offers those buttons only to someone whose tap would not be refused.

A credential that cannot be checked because the database does not answer gets
`503` with `Retry-After`, not `401`: an app treats 401 as signed out and forgets
its session, so a database restart used to sign out every paired phone.

**A mobile session** is a credential of its own: `Authorization: Bearer nxm_…`
or the `X-Mobile-Session` header. A phone is connected under Settings → Mobile
app: `POST /mobile/pairing`, made as the signed-in user, issues an
`XXXXX-XXXXX` code (one use, five minutes), which the app exchanges at
`POST /mobile/pairing/redeem` for a token. Redemption spends the same budget as
a password sign-in. The session acts as its user, with their teams, but with the
role capped at `responder`: a phone answers pages, it does not change
configuration. The app therefore calls the ordinary `/api/v1/alert-groups/*`
rather than mobile copies of them. Only the token's SHA-256 is stored; a session
unused for 30 days expires, and use extends it. An invalid mobile token is a
failed sign-in (401) even next to a valid cookie. A phone needs a user with a
role: one created without a role — as a notification recipient only, say —
signs in neither on the web nor from a phone, and its mobile session is 401 too.

A channel's `external_id` is what an inbound command names, so only one channel
per platform may carry it: a second one answers `409`. Commands from a shared
chat — a Telegram group, a Slack or Mattermost channel — run only when that chat
is bound to a channel here, whoever sends them; a one-to-one chat with the bot
needs no channel, because the person is the boundary.

### Alert groups

| Method | Path |
|---|---|
| GET | `/api/v1/alert-groups` |
| GET | `/api/v1/alert-groups/{id}` |
| GET | `/api/v1/alert-groups/{id}/timeline` |
| POST | `/api/v1/alert-groups/{id}/acknowledge`, `/resolve` |
| POST | `/api/v1/alert-groups/bulk-resolve`, `/bulk-acknowledge`, `/bulk-silence` |

The bulk endpoints take `{"group_ids": ["...", "..."]}`. `bulk-silence` also
accepts `duration_minutes` (in the body or as `?duration_minutes=`, default 60).
The responses carry `silenced`/`acknowledged`/`resolved`, `not_found` and
`skipped`.

A silence with a duration ends on its own: at `silenced_until` the worker returns
the group to `open` and runs its escalation chain again from the first step, as it
does when a maintenance window ends. `POST /api/v1/alert-groups/{id}/unacknowledge`
also runs the chain again from the first step.

### Alerts, notifications, history, debugging

| Method | Path |
|---|---|
| GET | `/api/v1/alerts`, `/api/v1/notifications` |
| GET | `/api/v1/delivery-attempts?notification_id=<id>&limit=&offset=` (paginated like the other lists) |
| GET | `/api/v1/history?from=&to=&integration=&severity=&status=&channel=&user=&team=` |
| POST | `/api/v1/routes/debug/{key}` — preview routing and notifications for a payload |
| POST | `/api/v1/escalations/run` — run a pass over due escalations by hand |

### Readiness

| Method | Path |
|---|---|
| GET | `/api/v1/readiness` — is this installation capable of waking anybody at all? |
| POST | `/api/v1/readiness/acknowledge` — accept the current blockers (admin) |
| POST | `/api/v1/backups/report` — record a completed backup (admin; see [BACKUP_RESTORE.md](BACKUP_RESTORE.md)) |

The report runs ten checks — database, worker, integrations, routing,
notification targets, schedule coverage, the team boundary, channel policy,
retention and backup age — and grades each `ok`, `warning` or `blocker`. A
**blocker** means alerts can arrive and wake nobody; a check that could not run is
reported as a blocker rather than as a success.

The two flags differ deliberately. `ready` is true only when nothing is in the
way. `production_ready` is the weaker claim used by the activation gate: true when
nothing is in the way **or** a named administrator has accepted exactly the
blockers listed. That acknowledgement stores a fingerprint of the blocker set, so
it stops applying the moment they change — it is a decision about known problems,
not a permanent way to switch a warning off.

Two inputs cannot be derived from domain state and are kept as installation
metadata: the worker heartbeat (written by the worker itself, so the answer does
not depend on whether the API and the worker run in one process or two) and the
last reported backup.

---

## The error format

Every error is `{"error": "message"}`. The codes: `400` validation, `401`
authentication required, `403` a write under read-only access, `404` not found,
`429` rate limit exceeded, `500` internal error, `503` database unavailable.

Every response carries an `X-Request-ID` header (passed in or generated) for
correlation with the structured logs.
