# nxs-anomaly Community

![nxs-anomaly](assets/horizontal.png)

**Turn monitoring alerts into an on-call response — in your own infrastructure.**

nxs-anomaly receives alerts from Prometheus Alertmanager, Grafana and webhooks,
groups repeated events, routes them to the person on call, and escalates until
someone acknowledges. Your team gets a shared alert timeline and a record of
notification attempts, acknowledgements and resolutions.

The backend is a Go binary with PostgreSQL; the web interface ships separately.
Community needs no message broker or cache and is released under Apache 2.0.

**[Start with Community](#quickstart)** ·
[Read the docs](#documentation) ·
[Compare editions](#community-and-enterprise) ·
[Discuss Enterprise with Nixys](https://nixys.io/contacts/)

## Introduction

### Who can use the tool?

- **SRE and DevOps teams** that already collect alerts and need on-call rotations,
  escalation policies and a clear owner for each response.
- **System administrators** who want to reach the duty engineer through chat,
  email or an Asterisk call instead of watching an alert feed manually.
- **Development teams** that need to receive, acknowledge and resolve their
  service alerts from one interface.
- **Platform teams** evaluating a shared alerting service. Start by proving the
  response workflow in Community; consider Enterprise when you need corporate
  sign-in, access boundaries between teams or an analytics event stream.

### Features

| What your team needs | What Community provides |
|---|---|
| Bring existing monitoring together | Ingestion from Prometheus Alertmanager, Grafana and generic JSON webhooks |
| Keep repeated events together | Deduplication and grouping by alert keys, with routing to escalation chains |
| Reach the current duty engineer | On-call rotations, timezones, schedule overrides and team-based escalation steps |
| Escalate an unanswered alert | Notification and wait steps, delivery retries and a dead-letter notification path |
| Use the channels your team already checks | Webhooks, Telegram, email, Slack/Mattermost-compatible endpoints and Asterisk calls |
| Act on an alert | Acknowledge, resolve and silence in the web interface; ChatOps actions where configured |
| Understand what happened | Alert timelines, audit history and delivery attempts |
| Operate the service yourself | Password sign-in, role-based access, REST API, Prometheus metrics, OpenTelemetry tracing and a production security profile |

The interface is available in English and Russian, with per-user language and
timezone preferences.

### How it works

```text
Alertmanager / Grafana / Webhooks
                |
                v
       Ingest, group and route
                |
                v
   On-call schedule + escalation chain
                |
                v
   Chat / Email / Webhook / Asterisk
                |
                v
       Acknowledge -> Resolve

PostgreSQL stores alert state, schedules and delivery history.
The worker runs escalations, notifications and retries.
```

Keep your existing monitoring: nxs-anomaly handles the response after an alert
fires. It does not collect or store your monitoring metrics.

## Quickstart

### Docker Compose — local evaluation

You need Git, Docker with Docker Compose, and access to GitHub Container Registry.
The stack starts PostgreSQL, the API, a worker and the web interface from release
images; Go and Node.js are only needed for a source build.

```bash
git clone https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
cp .env.example .env
```

Edit `.env` before starting:

| Variable | Set it to |
|---|---|
| `NXS_ANOMALY_VERSION` | A published tag from [Releases](https://github.com/nixys/nxs-anomaly/releases), including the `v` prefix |
| `POSTGRES_PASSWORD` | A strong database password; a randomly generated hexadecimal value avoids URL-encoding issues in the Compose DSN |
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` | Your initial administrator username (`admin` is prefilled) |
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD` | A strong administrator password |

```bash
docker compose up -d
docker compose ps
curl --fail http://127.0.0.1:8080/health
curl --fail http://127.0.0.1:8081/ready
```

The API should report `status: ok`, `db_ok: true` and `edition: community`.
The second URL checks the separate worker; its readiness confirms that the
background cycle can run.

Open [the web interface](http://127.0.0.1:3100) and sign in with your bootstrap
administrator. A fresh installation is empty: the next step connects your team
and alert source.

### Get your first notification

Open **Setup** in the interface and follow the checklist:

1. **Add your team.** Create the people who will receive alerts and their team.
2. **Give everyone a way to be reached.** Set each person's notification target
   and configure its transport. Provider credentials must reach the **worker**
   in this Compose deployment; editing `.env` alone does not pass additional
   variables to containers. See the [configuration guide](docs/community/en/CONFIGURATION.md)
   for channel settings. The default `log` target only writes a log entry.
3. **Put someone on call.** Create a rotation covering the current time and check
   that it resolves to the intended person.
4. **Define an escalation chain.** Add a notification step for the on-call team;
   add wait and fallback steps for unanswered alerts.
5. **Connect an alert source.** Create an integration and connect its route to
   that chain. Keep its integration key for the request below.
6. **Send a real test alert** from Setup, receive it on the selected channel,
   and acknowledge it in the interface.

To test the Alertmanager endpoint explicitly, replace the key and run:

```bash
INTEGRATION_KEY='replace-with-your-integration-key'
curl --fail-with-body -i \
  "http://127.0.0.1:8080/integrations/v1/alertmanager/${INTEGRATION_KEY}" \
  -H 'Content-Type: application/json' \
  -d '{"alerts":[{"status":"firing","fingerprint":"readme-first-alert",
        "labels":{"alertname":"ReadmeTest","severity":"critical","instance":"demo-1"},
        "annotations":{"summary":"First nxs-anomaly alert"}}]}'
```

Expect HTTP `202`, an alert group in the interface, and a notification after the
worker processes the escalation. A `202` confirms acceptance; check delivery and
acknowledgement separately. Repeat the request to check grouping, then send the
same payload with `"status":"resolved"` to close this test alert.

For ongoing use, configure your Alertmanager receiver to send to
`/integrations/v1/alertmanager/<integration key>` at an API address reachable
from Alertmanager. `127.0.0.1` only works when the sender shares that host's
network context.

If the group appears but no notification arrives, check **Setup** for missing
contacts or transports, then inspect `docker compose logs worker` and the
notification's delivery status. See [troubleshooting](docs/community/en/SETUP.md#when-something-does-not-work).

### Choose your deployment

| Environment | Next step |
|---|---|
| Local evaluation or a VM with Docker | Use Compose above; [Setup](docs/community/en/SETUP.md) explains configuration and source builds |
| Bare metal or a VM without Docker | Run the Go service against PostgreSQL and serve the web interface; see [From source](docs/community/en/SETUP.md#from-source) |
| Kubernetes | Use the [Helm chart](deploy/helm/nxs-anomaly) and [deployment guide](docs/community/en/DEPLOY.md#kubernetes); configure your database, secrets and ingress |
| Production | Plan external PostgreSQL, TLS, authentication, provider credentials, [hardening](docs/community/en/SECURITY_PROFILE.md) and [backup/restore](docs/community/en/BACKUP_RESTORE.md) |

The Compose example binds ports to loopback and uses HTTP for local evaluation.
Its named PostgreSQL volume survives ordinary `docker compose down` / `up`.
`docker compose down -v` deletes that data.

## Documentation

| I want to… | Read |
|---|---|
| Install and send a first alert | [Setup](docs/community/en/SETUP.md) |
| Configure people, channels, schedules and routing | [Configuration](docs/community/en/CONFIGURATION.md) |
| Understand grouping and escalation behavior | [Alert processing](docs/community/en/ALERT_PROCESSING.md) |
| Automate through the API | [API reference](docs/community/en/API.md) · [OpenAPI contract](docs/openapi.json) |
| Deploy and size an installation | [Deployment](docs/community/en/DEPLOY.md) · [Capacity](docs/community/en/CAPACITY.md) |
| Secure and recover the service | [Security profile](docs/community/en/SECURITY_PROFILE.md) · [Backup and restore](docs/community/en/BACKUP_RESTORE.md) |
| Monitor delivery and investigate failures | [Alerting rules](docs/community/en/ALERTING_RULES.md) · [Tracing](docs/community/en/TRACING.md) |

Browse all documentation in [English](docs/community/en) or
[Russian](docs/community/ru). Translated pages link to their counterpart.

## Community and Enterprise

**Community includes the core alerting and on-call workflow under Apache 2.0.**
Use it when the people operating an installation can share visibility and you
manage authentication locally. Teams and role-based permissions are included;
team membership in Community does not create an access boundary.

**Enterprise adds controls for running a shared service across an organization.**

| Capability | Community | Enterprise |
|---|---|---|
| Alert ingestion, grouping, routing and escalation | Included | Included |
| Teams, on-call schedules and notification channels | Included | Included |
| Password sign-in and role-based permissions | Included | Included |
| Audit history, REST API, Prometheus metrics and tracing | Included | Included |
| Self-hosted deployment with PostgreSQL | Included | Included |
| Corporate single sign-on through OIDC | — | Included; configure your identity provider |
| Access scoping by team | — | Included; enable and configure team scoping |
| Lifecycle event stream for external analytics | — | Included; export through Kafka for downstream processing, such as ClickHouse |
| Support | Community issues, best effort | Commercial support; scope and response terms agreed with Nixys |

### When to consider Enterprise

- **Your identity provider should control sign-in and role mapping.** Connect
  corporate authentication through OIDC.
- **Several teams share the installation but need different visibility.**
  Configure team access boundaries alongside the existing roles.
- **You need to analyze incident lifecycles in your data platform.** Export
  events to build reporting across alerts, acknowledgements and resolutions.
- **Your operations need an agreed support response.** Discuss deployment and
  support requirements with Nixys.

### Discuss your Enterprise deployment

**[Contact Nixys about nxs-anomaly Enterprise](https://nixys.io/contacts/)** or
use the [Nixys contact bot on Telegram](https://t.me/nixys_support_bot).
Ask about an Enterprise evaluation, deployment options and support terms.

To start the conversation, share:

- Your monitoring sources and how you deploy services: Compose, VMs or Kubernetes.
- The teams involved and the requirement driving your evaluation: SSO, access
  boundaries, analytics or support.
- Whether you already run Community, its version, and your intended rollout date.

You can validate ingestion, on-call routing and delivery in Community first.
Both editions share the domain model and database migrations. Plan the switch
with Nixys around compatible versions, a database backup, Enterprise artifacts
and feature configuration. The running edition is visible at `/health`; the
**Enterprise** page in the interface shows which capabilities are available or
still need configuration.

## Project status and feedback

nxs-anomaly is **pre-1.0**. Review [release notes](https://github.com/nixys/nxs-anomaly/releases)
and [migrations](docs/community/en/MIGRATIONS.md) before upgrading; API contracts
may evolve and migrations are forward-only.

Follow releases with GitHub's **Watch → Custom → Releases**. For development
priorities, browse [open issues](https://github.com/nixys/nxs-anomaly/issues) or
open a feature request describing your use case and expected behavior.

For bugs and Community questions, [open an issue](https://github.com/nixys/nxs-anomaly/issues)
with your version, deployment method, reproduction steps and relevant logs.
Community support is best effort; see [SUPPORT.md](SUPPORT.md).
For Enterprise evaluation and support arrangements, [contact Nixys](https://nixys.io/contacts/).
Report vulnerabilities privately using [SECURITY.md](SECURITY.md).

## Contributing

Bug reports, documentation improvements and pull requests are welcome.
[CONTRIBUTING.md](CONTRIBUTING.md) explains the commit format and required checks.

This public repository is generated from an internal source repository.
Maintainers apply accepted contributions upstream and publish them in a release
commit, crediting contributors as co-authors.

## License

nxs-anomaly Community is released under the [Apache License 2.0](LICENSE).
Enterprise features are distributed separately; contact Nixys for licensing terms.
