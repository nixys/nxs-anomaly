# The production security profile

*Русская версия: [SECURITY_PROFILE.md](../ru/SECURITY_PROFILE.md)*

`NXS_ANOMALY_PROFILE=production` moves every security-related default to its safe
value with one switch, so that a hardened installation does not depend on whether
somebody remembered a dozen separate flags. **Every value below can still be
overridden** by its own environment variable — an explicit setting always beats
the profile, so any single item can be opted out of.

| Setting | Default (no profile) | Under production | Override |
|---|---|---|---|
| SSRF guard (refuses private, loopback and link-local delivery addresses) | off | **on** | `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS` |
| Webhook ingest limit (tokens/s per integration key, **per pod**) | off (0) | **50** | `NXS_ANOMALY_WEBHOOK_RATE` |
| API limit (tokens/s per client IP, **per pod**) | off (0) | **20** | `NXS_ANOMALY_API_RATE` |
| Delivery circuit breaker (consecutive failures per channel/target) | off (0) | **5** | `NXS_ANOMALY_CIRCUIT_BREAKER_THRESHOLD` |
| New inline secrets in create/update requests | allowed | **refused** (`env:VARIABLE` required) | — (profile only) |

`NXS_ANOMALY_BLOCKED_CHANNELS` and `NXS_ANOMALY_EGRESS_ALLOWLIST` get no implicit
value even under the profile: which messengers and which external domains are
acceptable is an organisational decision the application cannot guess.

The session cookie carries `Secure` with or without the profile
(`NXS_ANOMALY_SESSION_COOKIE_SECURE`), and the management API refuses
unauthenticated requests with or without it until somebody opens it explicitly.
The profile weakens neither.

## What the SSRF guard closes

The URL this service will fetch is chosen by anyone who can edit an escalation
chain or a notification target. With the guard on, the check therefore has to
survive a hostile destination. It runs **in the HTTP transport**, not only over
the URL held in configuration, and that is what makes the following true:

| Attempt | Result |
|---|---|
| A URL pointing straight at a private, loopback or link-local address | refused before the request |
| An allowed host answers `302` towards `169.254.169.254` or an internal service | refused at connect time — every hop is checked |
| A name that resolves to a public address when checked and a private one when dialled (DNS rebinding) | refused — the connection goes to the address already checked, with no second resolution in between |
| A name that answers with a public **and** a private address at once | refused outright, rather than "take the public one" |
| A redirect to a non-HTTP scheme | refused |

Redirects between allowed hosts still work, up to the usual limit of ten hops, so
providers that answer `302` keep functioning.

With the guard off — the default outside the production profile — none of this
applies and delivery to a local address works as before, which is what
development environments rely on.

### The guard and the delivery proxy

If a channel is sent through a proxy (`NXS_ANOMALY_DELIVERY_PROXY_*`, see
[PROXY.md](PROXY.md)), the picture changes for that channel, and it is worth
deciding deliberately:

- **the proxy's own address is always allowed**, private included: an egress
  proxy on `10.x` is an ordinary arrangement, and the operator named it;
- **the destination's address is not checked at all** — the proxy resolves and
  dials it, not this service, so neither the up-front check nor the connect-time
  one could say anything meaningful (on a network with deliberately broken DNS
  the up-front check would additionally refuse everything);
- **what still applies**: `NXS_ANOMALY_EGRESS_ALLOWLIST`, against the written URL
  and at every redirect, and the refusal of non-HTTP schemes;
- **channels without a proxy** — including those listed in
  `NXS_ANOMALY_DELIVERY_NO_PROXY` — are checked in full, as described above.

Practical consequence: proxying makes sense for channels whose provider address
is fixed (telegram, slack, mattermost, mobile). For the `webhook` channel, where
the URL is set by whoever edits the chain, turn a proxy on only together with
`NXS_ANOMALY_EGRESS_ALLOWLIST`.

## Channel and egress policy

`NXS_ANOMALY_BLOCKED_CHANNELS=telegram,slack,mattermost` refuses a channel both
when the configuration is saved and immediately before delivery. The second check
exists for records created before the policy was turned on.

`NXS_ANOMALY_EGRESS_ALLOWLIST` accepts a comma-separated list of exact hostnames,
hostnames with a leading dot (a domain and its subdomains) and CIDRs. Once the
list is set, every outbound HTTP destination and every redirect must match it;
the private-address check keeps applying independently. Refusals carry explicit
reasons — `channel_blocked_by_policy` and `destination_not_allowlisted` — and are
never dressed up as a successful delivery.

## Personal data

Production readiness warns until retention horizons have been chosen for the
audit trail, notifications, delivery attempts and web sessions. It is a warning
and not an automatic policy: legally meaningful horizons are the operator's to
set. Export and erasure procedures, and the copies that live outside PostgreSQL,
are described in [DATA_INVENTORY.md](DATA_INVENTORY.md).

## Limits and replica count

The limiters count differently, and the difference starts to matter as the API
scales out.

| Limiter | Where the buckets live | Scope of the number you set |
|---|---|---|
| Webhook ingest (`NXS_ANOMALY_WEBHOOK_RATE`) | in the API process | **per pod** |
| Management API (`NXS_ANOMALY_API_RATE`) | in the API process | **per pod** |
| Sign-in attempts (not configurable) | PostgreSQL | **cluster-wide** |

The first two sit on the hot path, where a database round trip per accepted alert
would cost more than the protection is worth; they exist so that one process does
not drown, not to hold a cluster-wide quota. So with `api.replicaCount: 4` and
`NXS_ANOMALY_WEBHOOK_RATE=50` the installation as a whole accepts 200/s.

The chart takes the number you actually want and divides it itself:

```yaml
api:
  replicaCount: 4
rateLimits:
  webhookRatePerCluster: 100   # renders as NXS_ANOMALY_WEBHOOK_RATE=25
  apiRatePerCluster: 40        # renders as NXS_ANOMALY_API_RATE=10
```

`NXS_ANOMALY_WEBHOOK_RATE` set directly through `config`/`extraConfig` still
works and is passed through unchanged — as a per-pod number.

The sign-in limiter is built differently on purpose. A per-pod limit on password
attempts means an attacker spreading a guess across pods gets N times the
attempts: the one limit that most needs to be global would scale up together with
the installation. Its buckets live in `nxs_anomaly_rate_buckets` (migration
0024), and the limit itself — five attempts, then one every ten seconds, per
client IP — applies across every replica at once. Only **failed** attempts are
charged: a successful sign-in returns its token, so a shared egress IP does not
lock out its own people.

If PostgreSQL is unavailable the sign-in limiter **lets the request through**
rather than refusing it. That is deliberate: the sign-in path needs the database
to check a password at all, so refusing "just in case" would turn a database
outage into a total sign-in outage while protecting nothing. The event is logged
as `rate_limiter_unavailable`.

## Secret references (`env:VARIABLE`)

Per-object secrets held in the database — an integration's `webhook_secret`, a
ChatOps channel's `webhook_url`, a mobile device's `push_token` — can be written
as a reference instead of a literal:

```json
{ "name": "acme", "webhook_secret": "env:NXS_ANOMALY_ACME_HMAC" }
```

The value is read from the named environment variable at the moment it is used,
so the plaintext never sits in PostgreSQL. Under the production profile a **new**
inline secret — one that is not a reference — is refused with a clear error;
legacy values already in the database keep working, so upgrading a version does
not break an installation and they can be moved to references at your own pace.

## Confirming the profile is on

Both the API and the worker log their effective security configuration at
startup. A quick check that the guard is live:

```bash
# under the profile, delivery to a loopback webhook is refused (SSRF guard)
NXS_ANOMALY_PROFILE=production nxs-anomaly serve ...
```

The behaviour is covered by a security regression suite
(`internal/{utils,engine,server}/*security*_test.go`, `*profile*_test.go`) that
runs in CI.
