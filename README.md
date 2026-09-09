# nxs-anomaly

Alerting and on-call for teams that already run Prometheus: it receives alerts,
groups them, decides who is on call, escalates until somebody answers, and keeps
a record of what it did.

One Go binary and PostgreSQL. No message queue, no cache, no Python runtime, no
Kubernetes operator — a `docker compose up` away from a working installation.

```bash
git clone https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
cp .env.example .env          # set NXS_ANOMALY_VERSION (a release tag, e.g.
                               # v0.1.88), a database password and an admin
                               # password — docker compose refuses to start
                               # with any of them left blank
docker compose up -d
```

This pulls the published, signed images for that release — no local Go or npm
build. To build from source instead, use `docker-compose.dev.yml` and
`.env.dev.example` in its place; see [SETUP.md](docs/community/en/SETUP.md).

The interface is on <http://127.0.0.1:3100>, the API on
<http://127.0.0.1:8080>. Sign in with the admin account from your `.env`.
Point Alertmanager at `/integrations/v1/alertmanager/<integration key>` and the
first alert will open a group, page whoever the schedule says is on call, and
appear on the alert page.

PostgreSQL data lives on a named volume and survives an ordinary `down`/`up`;
`docker compose down -v` is the deliberate way to discard it.

## What it does

- **Receives** from Prometheus Alertmanager, Grafana, and plain webhooks, and
  from anything that can POST JSON.
- **Groups** alerts by a deduplication key so a flapping service is one incident
  and not two hundred notifications.
- **Routes** each group through matchers to an escalation chain.
- **Escalates** on a schedule — notify a person, wait, notify the next, notify a
  whole team — until somebody acknowledges.
- **Delivers** through webhooks, Telegram, e-mail, Slack- and
  Mattermost-compatible endpoints, and telephone calls via Asterisk, with
  retries, a dead-letter path and a circuit breaker per destination.
- **Answers back**: acknowledge and resolve from the chat message, from the web
  interface, from the mobile API, or from the source that raised the alert.
- **Records** every transition in a timeline and an audit trail, and exports
  Prometheus metrics and OpenTelemetry traces.

## What it is not

- Not a metrics store. It reacts to alerts; Prometheus keeps the data.
- Not a status page.
- Not multi-region. One PostgreSQL is the boundary of an installation.
- Not 1.0 yet. The API is versioned and the migrations are forward-only, but the
  contract may still gain fields.

## Editions

This repository is the community edition, and it is the whole product for a
single team: everything above is here, under Apache 2.0.

The enterprise edition adds what larger installations ask for — single sign-on
through an OIDC provider, team boundaries that limit what each team sees, and an
analytics event stream feeding a data warehouse. A running installation reports
which edition it is at `/health`, and the interface shows the features it does
not have rather than hiding them. `docs/` describes what each one covers; ask the
maintainers for access to the enterprise build.

## Documentation

| | |
|---|---|
| [SETUP.md](docs/community/en/SETUP.md) | Local installation, environment variables, first alert |
| [openapi.json](docs/openapi.json) | The machine-readable API contract |
| [SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md) | The hardened defaults and what each one refuses |
| [TRACING.md](docs/community/en/TRACING.md) | OpenTelemetry: what is instrumented and why |
| [MIGRATIONS.md](docs/community/en/MIGRATIONS.md) | The schema, how it changes, and how to add to it |

The rest — configuration, deployment, the domain model, the API reference — is
written first in Russian and translated from there. Every page in
[docs/community/en](docs/community/en) has a counterpart in
[docs/community/ru](docs/community/ru), and each links to the other at the
top; a newly added Russian page may lag behind by a release until it is
translated, and is absent here rather than machine-translated in the meantime.

## Deployment

A Helm chart lives in `deploy/helm/nxs-anomaly`. It ships PostgreSQL for
development and expects a managed one in production, and it carries the
NetworkPolicy, PodDisruptionBudget, ServiceMonitor and PrometheusRule objects a
cluster deployment needs.

```bash
helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --set existingSecret.enabled=true --set existingSecret.name=nxs-anomaly-env \
  --set externalPostgres.host=postgres.internal
```

Read the chart's own README in `deploy/helm/nxs-anomaly` before the first
production install: it lists the decisions that have no safe default.

## Support

Issues are welcome at <https://github.com/nixys/nxs-anomaly/issues>, with no
service-level promise attached — see [SUPPORT.md](SUPPORT.md) for what that means
in practice and which parts of the deployment surface are covered.

Security reports do **not** go to the issue tracker.
[SECURITY.md](SECURITY.md) says where they go.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) covers commit format and the checks a change
has to pass. One thing to know before you open a pull request: this repository is
published from an internal one, so a merged change is applied upstream by a
maintainer and arrives here in the next release commit with you credited as
co-author. Your name stays on the work; your commit does not survive verbatim.

## License

Apache 2.0 — see [LICENSE](LICENSE).
