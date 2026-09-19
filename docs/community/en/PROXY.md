# A proxy for outbound delivery

*Русская версия: [PROXY.md](../ru/PROXY.md)*

This document is about one question: **how to send notifications when the
provider is unreachable from the network the service runs in**. A blocked
messenger, egress permitted only through one audited hop, a cluster closed to the
outside — from delivery's point of view these are the same thing.

Without a proxy such a notification behaves predictably badly: it fails at
connect, goes to retry, exhausts its attempts and lands in the dead letter. The
alert never arrives, and in the timeline it looks like a problem on the
provider's side.

It is configured through environment variables, like the rest of the delivery
credentials. That is deliberate: one decision per installation, living where the
deployment is described rather than in the database or the interface. No
migrations, no objects, no separate permissions.

## The variables

| Variable | What it sets |
|---|---|
| `NXS_ANOMALY_DELIVERY_PROXY_URL` | the default proxy for every delivery channel |
| `NXS_ANOMALY_DELIVERY_PROXY_<CHANNEL>_URL` | an override for one channel |
| `NXS_ANOMALY_DELIVERY_NO_PROXY` | hosts always dialled directly |

`<CHANNEL>` is one of `TELEGRAM`, `SLACK`, `MATTERMOST`, `WEBHOOK`, `CHATOPS`,
`MOBILE`, `ISSUE`, `EMAIL`, `CALL`. The `log` channel has no transport and is
absent from the list.

The value `direct` (synonyms `none`, `off`) turns the proxy off for one channel
when a general one is set. An empty value means "inherit the general one".

`NXS_ANOMALY_DELIVERY_NO_PROXY` uses `NO_PROXY` syntax: exact hosts and domains
separated by commas, where a domain covers its subdomains (`.corp.example` and
`corp.example` are equivalent and both match `api.corp.example`), and `*` means
everything.

The variables are read once at startup, and **both the API and the worker** must
get the same set: the worker performs delivery, but a test notification and the
replies to Telegram button presses go out from the API.

### Schemes

| Scheme | Where it applies |
|---|---|
| `http`, `https` | HTTP channels. HTTPS destinations go through `CONNECT` |
| `socks5`, `socks5h` | HTTP channels **and** the non-HTTP `email` (SMTP) and `call` (Asterisk AMI) channels |
| `tcp` | A transparent TCP relay (HAProxy `mode tcp`). Every channel, `email` and `call` included — see below |

Credentials go in the URL: `socks5://user:pass@tunnel.internal:1080`. Default
ports are 80 for `http`, 443 for `https`, 1080 for both SOCKS schemes. **`tcp` has
no default port** — whoever runs the relay chooses it, and guessing would return
a connection error instead of an answer.

### A transparent relay (`tcp://`) — HAProxy in `mode tcp`

The most common way around a block is not a proxy but a bare TCP relay on an
outside machine:

```haproxy
frontend telegram_api
    bind *:443
    mode tcp
    default_backend telegram_api_backend

backend telegram_api_backend
    mode tcp
    server telegram api.telegram.org:443 check
```

Such a hop **speaks no proxy protocol at all**: no `CONNECT`, no SOCKS
handshake, and it does not terminate TLS — it simply moves bytes to its single
backend. So the `tcp://` scheme changes exactly one thing: the address the socket
is opened to.

```bash
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL='tcp://relay.example.com:443'
```

Everything else stays the provider's: the request path, the `Host` header, **the
SNI and the certificate check**. The TLS session is established through the relay
to the real `api.telegram.org`, and the certificate is verified against it rather
than against the relay. Disabling verification is unnecessary and wrong: the
relay has nothing to present, because it decrypts nothing.

**A relay is blind to the destination.** It sends everything it receives to its
one backend, regardless of where the request was addressed. That suits a channel
with a fixed provider host (`telegram`, `slack`, `mattermost`, `mobile`). For
channels whose address is written into the escalation chain — `webhook`, `issue`,
`chatops` — startup logs a warning,
`delivery_proxy_relay_on_variable_destination`: every destination of that channel
will end up at the same server. It is a warning and not a refusal: a single
internal receiver is a legitimate configuration.

**A relay carries `email` and `call`.** Unlike an HTTP proxy it works below the
protocol, so SMTP and AMI pass through it as they do through SOCKS5. `NO_PROXY`
keeps its meaning: a listed host is dialled directly by name instead of going to
the relay's backend.

**`socks5` and `socks5h` behave identically here: the provider's hostname is
always resolved by the proxy, not by the service.** That is not a simplification
but a requirement of the problem: a network that blocks a provider usually breaks
its DNS too, and local resolution in such a network kills delivery before the
connection begins.

## Typical configurations

**Only the messengers are blocked; everything else goes direct:**

```bash
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL='socks5://tunnel.internal:1080'
NXS_ANOMALY_DELIVERY_PROXY_SLACK_URL='socks5://tunnel.internal:1080'
```

**Everything outbound through an egress proxy, the internal receiver direct:**

```bash
NXS_ANOMALY_DELIVERY_PROXY_URL='http://egress.internal:3128'
NXS_ANOMALY_DELIVERY_NO_PROXY='receiver.internal,.corp.example'
```

**The same, but the `webhook` channel needs no proxy at all:**

```bash
NXS_ANOMALY_DELIVERY_PROXY_URL='http://egress.internal:3128'
NXS_ANOMALY_DELIVERY_PROXY_WEBHOOK_URL='direct'
```

**A proxy with a password** — the variable holds a secret, so in Kubernetes it
belongs in the application's Secret rather than the ConfigMap (see the chart note
below):

```bash
NXS_ANOMALY_DELIVERY_PROXY_URL='socks5://svc:s3cret@tunnel.internal:1080'
```

### Helm

```yaml
deliveryProxy:
  url: "http://egress.internal:3128"
  channels:
    telegram: "socks5://tunnel.internal:1080"
    webhook: "direct"
  noProxy: "receiver.internal,.corp.example"
```

The section renders into the shared ConfigMap, so both the API and the worker
read it. **The chart refuses to render a URL with a password**: a ConfigMap is
readable by anyone with `get` in the namespace, and quietly publishing proxy
credentials there is worse than failing. Such a URL goes into the application's
Secret under the same variable name; the Secret is applied after the ConfigMap
and overrides it.

Egress to the proxy is already permitted by the chart's NetworkPolicy — the
delivery pod reaches the outside network anyway.

## What goes where, per channel

| Channel | What it talks to |
|---|---|
| `telegram` | `api.telegram.org`, including replies to inline button presses |
| `slack`, `mattermost` | the platform's incoming webhook |
| `webhook` | the address from the `TRIGGER_WEBHOOK` step or the notification target |
| `chatops` | the ChatOps channel's `webhook_url` |
| `mobile` | the operator's push relay (`NXS_ANOMALY_MOBILE_PUSH_URL`) |
| `issue` | Redmine or a generic tracker from a chain step |
| `email` | an SMTP server — **SOCKS5 or a relay only** |
| `call` | Asterisk AMI on port 5038 — **SOCKS5 or a relay only** |

The dead-letter notification (`NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL`) also goes
through the `webhook` channel's proxy.

**ChatOps uses its platform's proxy.** A Telegram ChatOps channel writes to
`api.telegram.org`, so with `..._TELEGRAM_URL` set it uses that one — otherwise
an operator who configured a proxy for a blocked Telegram would get working
personal notifications and a silent ChatOps channel. An explicit
`..._CHATOPS_URL` — `direct` included — beats that inference.

**`email` and `call` are not HTTP.** An HTTP proxy is a request-level mechanism,
and SMTP and AMI have no requests. If an HTTP proxy applies to those channels —
inherited from the general `..._PROXY_URL`, say — it is **ignored**, and startup
logs `delivery_proxy_ignored_for_tcp_channel`. Usually that is what you want: a
mail relay and telephony are most often inside the perimeter. If they are not,
give them `socks5://` or `tcp://` explicitly: both work below the protocol and
carry any TCP.

## A configuration error does not become "send it directly"

A channel whose proxy URL failed to parse **fails on every attempt**, with a
reason, rather than quietly going direct:

```
delivery proxy for channel "telegram" is misconfigured: unsupported proxy scheme "htp"
```

And `GET /api/v1/readiness` counts such a channel as a channel **with no
transport** — exactly like Telegram without a bot token.

That is deliberate. Silently falling back to a direct send on a network where the
provider is blocked is a lost alert that looks like a provider problem, and it
will be investigated in the wrong place.

## Interaction with the SSRF guard

With `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS=true` — the default under the production
profile — the picture for a **proxied** channel differs, and it is worth
accepting deliberately:

- **the proxy's own address is always allowed**, private included. An egress
  proxy on `10.x` or a sidecar on loopback is an ordinary arrangement, and the
  operator named it, not whoever edits an escalation chain;
- **the destination is checked as far as the service can see it.** The proxy
  resolves and dials it, so there is no connect-time check; up front, an IP
  literal in a private, loopback or link-local range is refused, and so is a
  name that resolves locally to one. A name with no local answer is let
  through — on a network with deliberately broken DNS that is the normal case —
  and a name only the proxy maps to an internal address is beyond what the
  service can judge; `NXS_ANOMALY_EGRESS_ALLOWLIST` is the control for that.
  Before 1.4.3 the proxied destination was not checked at all, so a webhook to
  `http://169.254.169.254/` reached the proxy host's metadata service;
- **still in force**: `NXS_ANOMALY_EGRESS_ALLOWLIST`, against the written URL and
  at every redirect, plus the refusal of non-HTTP schemes and the ten-hop limit;
- **channels without a proxy are checked in full**, as described in
  [SECURITY_PROFILE.md](SECURITY_PROFILE.md). That includes destinations listed
  in `NXS_ANOMALY_DELIVERY_NO_PROXY`: no proxy is used for them, and the whole
  guard — DNS rebinding and cloud-metadata redirects included — applies as
  before.

Practical consequence: proxy without reservation the channels with a fixed
provider address — `telegram`, `slack`, `mattermost`, `mobile`. For `webhook`,
where the URL is set by whoever edits the chain, turn a proxy on together with
`NXS_ANOMALY_EGRESS_ALLOWLIST`. Mind that `NXS_ANOMALY_DELIVERY_PROXY_URL` is the
default for **every** channel, `webhook` and `issue` included; to proxy only
the providers, set the per-channel variables instead.

## Checking it

**1. Startup.** The service logs what it understood, with the password stripped:

```
INFO delivery_proxy_configured channel=telegram proxy=socks5://svc:xxxxx@tunnel.internal:1080 no_proxy=receiver.internal
WARN delivery_proxy_ignored_for_tcp_channel channel=email ...
ERROR delivery_proxy_invalid channel=slack error=...
```

**2. Readiness.** A channel with an unreadable proxy shows up as a channel with
no transport, in the "everyone on call can be reached" check:

```bash
curl -s "$API/api/v1/readiness" | jq '.checks[] | select(.key=="notification_targets")'
```

**3. A real send.** A test notification to a user, or a whole alert:

```bash
curl -X POST "$API/integrations/v1/webhook/<key>" \
  -H 'Content-Type: application/json' \
  -d '{"title":"proxy check","severity":"critical","status":"firing"}'
```

The provider's answer lands in the delivery attempts, together with the status
code and an excerpt of the body:

```bash
curl "$API/api/v1/delivery-attempts?alert_group_id={id}"
```

**4. Locally, with no proxy server.** A SOCKS5 proxy is one command away through
any SSH host you have:

```bash
ssh -N -D 127.0.0.1:1080 user@jump-host
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL='socks5://127.0.0.1:1080'
```

The local equivalent of the HAProxy relay is one `socat` command, then `tcp://`:

```bash
socat TCP-LISTEN:8443,fork,reuseaddr TCP:api.telegram.org:443 &
NXS_ANOMALY_DELIVERY_PROXY_TELEGRAM_URL='tcp://127.0.0.1:8443'
```

That the relay really passes through can be checked without a bot — the real Bot
API answers a deliberately invalid token:

```bash
curl --resolve api.telegram.org:8443:127.0.0.1 \
  'https://api.telegram.org:8443/bot123456:INVALID/getMe'
# {"ok":false,"error_code":401,"description":"Unauthorized"}
```

A 401 from Telegram itself means TLS was established through the relay and the
certificate matched. A hanging handshake means the other end is not `mode tcp`
but something terminating TLS — nginx routing by `Host`, for instance — and the
`tcp://` scheme does not suit such a host.

## Common mistakes

| Symptom | Cause |
|---|---|
| `delivery proxy for channel ... is misconfigured` | A typo in the URL, or an unsupported scheme. The service deliberately does not fall back to a direct send |
| A proxy is set but `email`/`call` bypasses it | Only `socks5://` and `tcp://` work for them; an HTTP proxy is ignored (see `delivery_proxy_ignored_for_tcp_channel`) |
| `tcp relay ... needs an explicit port` | The `tcp` scheme has no default port: `tcp://relay.example.com:443` |
| `webhook` notifications reach the wrong recipient | A `tcp://` relay was assigned to a channel with a variable address — it is blind to the destination (see `delivery_proxy_relay_on_variable_destination`) |
| `tls: failed to verify certificate` through a `tcp://` relay | The relay leads to a different provider, or TLS is terminated somewhere on the way. Verification is against the provider's host — that is the point of the scheme, and it should not be turned off |
| The ChatOps channel is silent while personal notifications work | `..._CHATOPS_URL=direct` was set explicitly, and it beats the platform's proxy |
| `blocked webhook host ... resolves to non-public address` with a proxy configured | The host is in `NXS_ANOMALY_DELIVERY_NO_PROXY` and went direct, where the guard applies in full |
| `destination_not_allowlisted` | Nothing to do with the proxy: the address is not in `NXS_ANOMALY_EGRESS_ALLOWLIST`, which is checked for proxied channels too |
| The chart fails to render with "a delivery proxy URL with a password…" | A URL with a password cannot go into a ConfigMap — move the variable into the application's Secret |
| Notifications go direct although the variables are set | The variables did not reach the **worker** deployment: it is the one that delivers |

## Related documents

- [CONFIGURATION.md](CONFIGURATION.md) — configuring the installation as a whole.
- [SECURITY_PROFILE.md](SECURITY_PROFILE.md) — the SSRF guard, the allowlist,
  channel policy.
- [SETUP.md](SETUP.md) — the environment variables.
