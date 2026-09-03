# Personal data inventory

*Русская версия: [DATA_INVENTORY.md](../ru/DATA_INVENTORY.md)*

What nxs-anomaly holds about people, where exactly, what bounds it in time, and
what can be done with it when somebody asks.

This document describes the **product**, not any particular installation.
Controller and processor roles, legal bases and contractual obligations are
outside it: they are decided by whoever deploys the system. What is here is the
factual map of the data those documents rest on.

Three things worth understanding before the table:

1. **The product does not collect personal data by itself.** Everything below
   arrives one of three ways: an administrator created a responder, a person
   signed in, or a monitoring system sent an alert with arbitrary contents. The
   third channel is the awkward one: an alert payload is data nobody classified,
   and it has to be treated as potentially personal.
2. **The default retention is "forever".** That is deliberate — an upgrade must
   not quietly start deleting incident history — but for an installation holding
   personal data, the absence of a horizon is not a default, it is a decision not
   yet made. The `values-beta-rf.yaml` preset sets horizons explicitly, and the
   `data_retention` readiness check warns until they exist.
3. **Copies outside the database live by their own rules.** Backups, logs and
   traces are independent stores with their own retention, access and geography.
   Deleting a user does not touch them; the erasure report names them explicitly
   rather than passing over them.

---

## The map

### 1. Identifying a person

| What | Where | How it gets there | What bounds it |
|---|---|---|---|
| Name / display name | `nxs_anomaly_users.data->>'name'` | creating a user | lives as long as the user does; deleted or pseudonymised by erasure |
| Login | `nxs_anomaly_users.username` | the same | the same |
| E-mail | `nxs_anomaly_users.email` | the same; used as the `email` channel's delivery address | the same |
| Phone | `nxs_anomaly_users.data->>'phone'` | the same; the `call` channel's delivery address | the same |
| Telegram id | `nxs_anomaly_users.data->>'telegram_id'` | the same; the `telegram` channel's delivery address | the same |
| Password hash | `nxs_anomaly_user_credentials.password_hash` | setting a password | kept out of `users.data` on purpose (migration 0019): that JSONB is returned verbatim through `/api/v1/users`. **Never included in an export** — it is a credential, not information about a person; the export reports only that one exists. |

### 2. Sessions and addresses

| What | Where | What bounds it |
|---|---|---|
| Client IP, User-Agent | `nxs_anomaly_web_sessions.request_ip / user_agent` | `NXS_ANOMALY_WEB_SESSION_RETENTION_DAYS`; expired ones are removed separately, a day after expiry |
| Session token hash | `nxs_anomaly_web_sessions.token_hash` | the SHA-256 is stored, not the token, so a database dump does not hand over live sessions. Not included in an export. |
| Request IP in the audit trail | `nxs_anomaly_audit_events.request_ip` | `NXS_ANOMALY_AUDIT_RETENTION_DAYS`; replaced with an empty string on erasure |
| Mobile devices and push tokens | `nxs_anomaly_mobile_devices`, `nxs_anomaly_mobile_sessions` | deleted on erasure; no separate horizon — the record exists while the device does |

### 3. Notifications

| What | Where | What bounds it |
|---|---|---|
| Delivery address (chat id, e-mail, number) | `nxs_anomaly_notifications.data->>'target'` | `NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS`; replaced by a pseudonym on erasure — **the row stays** |
| Who was woken, and when | `nxs_anomaly_notifications` (`user_id`, `status`, timestamps) | the same; erasure leaves it alone — it is a fact, not personal data |
| Provider response, error text, timings | `nxs_anomaly_notification_delivery_attempts` | `NXS_ANOMALY_DELIVERY_ATTEMPT_RETENTION_DAYS` |
| ChatOps message mirror | `nxs_anomaly_chatops_messages` | `NXS_ANOMALY_CHATOPS_MESSAGES_TTL_DAYS` |

### 4. Alerts

| What | Where | What bounds it |
|---|---|---|
| The alert payload: title, labels, arbitrary source fields | `nxs_anomaly_alerts.data`, `nxs_anomaly_alert_groups.data` | `NXS_ANOMALY_ALERT_GROUP_TTL_DAYS` (resolved groups are removed together with their alerts) |

This is the one category whose contents the product does not control. Hostnames,
account names, query text, sometimes client addresses — whatever the monitoring
system put in the alert arrives here verbatim. The practical consequence: the
retention horizon for alert groups *is* the retention horizon for unclassified
data, and it should be chosen on that basis rather than on how convenient long
investigations are.

### 5. The audit trail

| What | Where | What bounds it |
|---|---|---|
| Who did what, to what | `nxs_anomaly_audit_events` | `NXS_ANOMALY_AUDIT_RETENTION_DAYS` |
| Actor name | `actor_name` | replaced by a pseudonym on erasure |
| Request IP | `request_ip` | cleared on erasure |

The table is append-only, enforced by a trigger. Exactly two paths write to it
differently — the retention sweep and pseudonymisation on user deletion — and
both disable the trigger **inside their own transaction**, so a failure leaves it
enabled. No request path leads there. The reasoning is in the comments of
`internal/store/store_audit.go` and `store_erasure.go`.

### 6. Logs and traces

| What | Where | What bounds it |
|---|---|---|
| The service's structured logs (`LOG_FORMAT=json`) | stdout → the installation's log collector | the **log store's** retention, not the product's |
| Traces (with `tracing.enabled`) | an OTLP collector | the collector's retention |

Logs carry object identifiers, integration keys and — at `debug` level —
notification text. Traces carry integration keys, group ids and timings. Both
systems are part of the personal-data perimeter, and the decision about where
they live is taken together with the decision about the database. That is why the
`values-beta-rf.yaml` preset offers exactly two states for tracing: off, or
exporting to a collector inside the same perimeter.

### 7. Backups

A backup contains exactly what the live database contains, everything above
included, and lives by its own retention. Deleting a user does not extend to
backups: restoring one taken before the deletion brings the data back. That limit
is named in the erasure report (`out_of_scope`) and has to be accounted for in
the procedure — either backup retention is bounded, or the erasure is repeated
after a restore.

Procedures and checks: [BACKUP_RESTORE.md](BACKUP_RESTORE.md),
`tests/pitr_drill.sh`.

### Export

```
GET /api/v1/users/{id}/export        # admin only
```

It is assembled by reading the records rather than paraphrasing them: a person
should see what is actually stored. The sections are the user record, teams, web
sessions, notifications and delivery attempts, mobile devices and sessions,
ChatOps channels, the schedules they appear in, and two slices of the audit
trail — what they did, and what was done to them. Both slices are bounded by
`audit_export_limit`, and that number is returned in the response so that
truncation is visible as truncation.

The password hash and session token hashes are never exported.

The endpoint is admin-only despite being a GET: the ordinary read threshold would
let any authenticated user download somebody else's paging history.

### Erasure

```
POST /api/v1/users/{id}/erase        # admin only
```

| Action | Applied to |
|---|---|
| Deleted | credentials, web sessions (which hold IPs), mobile devices and sessions, the person's ChatOps channels |
| Pseudonymised | the user record, delivery addresses in notifications and attempts, `actor_name` and `request_ip` in the audit trail |
| Left alone | the events themselves: what happened, when, in what order, under an opaque id |

The user record remains as a tombstone (`erased_at`, empty role, not on duty).
The reason is not convenience: deleting the row outright would orphan the alert
history and destroy the audit of what that person did **to other people's data**
— which is precisely what the audit trail exists for.

The response is a verification rather than a promise: after writing, the data is
read back and searched for the original identifiers.

```json
{
  "verified": true,
  "residue": [],
  "counts": {"web_sessions_deleted": 1, "audit_events_pseudonymised": 12},
  "out_of_scope": ["backups …", "log pipeline …", "traces …"]
}
```

The operation is idempotent: calling it again re-verifies and answers
`already_erased: true`. The erasure itself is written to the audit trail.

---

## Restricting external channels

Telegram, Slack, Mattermost and arbitrary webhooks send outward who is on call
now and what is breaking at a customer. Two independent levers:

```yaml
config:
  # Channels forbidden installation-wide.
  NXS_ANOMALY_BLOCKED_CHANNELS: "telegram,slack,mattermost"
  # Where delivery may go at all: hostnames (a leading dot covers subdomains)
  # and/or CIDRs. Empty means no restriction.
  NXS_ANOMALY_EGRESS_ALLOWLIST: ".example.com,10.20.0.0/16"
```

The check sits in three places, and that is not duplication:

- **at configuration time** — a target on a forbidden channel cannot be saved,
  or the operator would learn about the ban on the night nobody was woken;
- **at delivery time** — objects configured before the policy tightened still do
  not go out; the notification ends in a terminal `skipped` with a reason
  (`channel_blocked_by_policy` / `destination_not_allowlisted`) rather than in
  "nothing happened";
- **on redirects** — a 302 leads somewhere the installation never agreed to, so
  the allowlist is checked at every hop.

The allowlist judges the address **as written**; the SSRF guard
(`NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS`) follows DNS and judges what the name
resolved to. They complement each other: an allowed name pointing inside the
cluster is still refused.

The `channel_policy` readiness check is a **blocker** while any user, escalation
step or ChatOps channel points at a forbidden channel or a disallowed address.
Not because something would leak — it would not — but because in the roster it
looks like a way to reach a person and is not one.

---

## What this document does not settle

- Legal bases, notifications, and the contractual division of roles.
- Localisation: where the database, the backups, the log store and the trace
  collector physically stand. The
  [values-beta-rf.yaml](../../../deploy/helm/nxs-anomaly/values-beta-rf.yaml)
  preset fixes the shape — external PostgreSQL with TLS and PITR, tracing either
  off or inside the perimeter — but geography cannot be verified from a chart.
- The procedure for responding to a data incident.
