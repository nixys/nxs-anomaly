# nxs-anomaly Community

[![GitHub release](https://img.shields.io/github/v/release/nixys/nxs-anomaly)](https://github.com/nixys/nxs-anomaly/releases)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/nxs-anomaly)](https://artifacthub.io/packages/helm/nxs-anomaly/nxs-anomaly)
[![Terraform Registry](https://img.shields.io/badge/Terraform-Registry-7B42BC?logo=terraform&logoColor=white)](https://registry.terraform.io/providers/nixys/nxs-anomaly/latest)
[![CI](https://github.com/nixys/nxs-anomaly/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/nixys/nxs-anomaly/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/nixys/nxs-anomaly)](LICENSE)

![nxs-anomaly](assets/horizontal.png)

**Self-hosted on-call schedules, alert routing and escalation for your existing monitoring.**

Receive alerts from Prometheus Alertmanager, Grafana or JSON webhooks, notify the
person on call, and escalate until someone acknowledges. Follow notifications,
acknowledgements and resolutions in one shared timeline.

**Apache 2.0 · Go + PostgreSQL · No message broker or cache required · English and Russian UI**

## Table of Contents

- [Introduction](#introduction)
- [Quickstart](#quickstart)
- [Documentation](#documentation)
- [Roadmap](#roadmap)
- [Feedback](#feedback)
- [Contributing](#contributing)
- [License](#license)

[Документация на русском](docs/community/ru/INSTALLATION.md)

## Introduction

### Features

- **Reach the duty engineer:** rotations, timezones, schedule overrides and escalation chains.
- **Use your team's channels:** Telegram, email, webhooks, Slack/Mattermost-compatible endpoints and Asterisk calls.
- **Handle repeated alerts:** grouping, deduplication, acknowledgement, resolution and silencing.
- **See what happened:** alert timelines, delivery attempts, audit history, Prometheus metrics and tracing.
- **Run it your way:** Docker Compose, Kubernetes with Helm, or a Go service with PostgreSQL; configure resources through the API or Terraform.

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

### When to consider Enterprise

- **Your identity provider should control sign-in and role mapping.** Connect
  corporate authentication through OIDC.
- **Several teams share the installation but need different visibility.**
  Configure team access boundaries alongside the existing roles.
- **You need to analyze incident lifecycles in your data platform.** Export
  events through Kafka for downstream analytics. With ClickHouse configured,
  on-call quality reports help identify overdue acknowledgements, night-time
  load, repeated escalations and noisy sources, with links to the affected alerts.
- **Your operations need an agreed support response.** Discuss deployment and
  support requirements with Nixys.

### How it works

![Alert flow: ingestion and routing, on-call escalation, notification delivery, acknowledgement and resolution](assets/nxs-anomaly-alert-flow.svg)

## Quickstart

**Try the demo first.** One command starts a ready-to-explore installation with a
team, an on-call rotation, an escalation chain, a webhook integration and an alert
already escalating:

```bash
docker compose -f deploy/demo/compose.yaml up -d
```

Open http://localhost:3100 and sign in as `alice` / `demo-password`, the engineer on
call. Notifications are written to the worker log (`docker compose -f deploy/demo/compose.yaml logs worker`);
send more alerts to the webhook URL shown on the Integrations page.
Remove the demo with `docker compose -f deploy/demo/compose.yaml down -v`. The
credentials are public and the stack is for evaluation only — use the installation
guides below for a real deployment.

| Deployment | Instructions and ready-to-use files |
|---|---|
| Bare metal / VM without containers | [On-premise installation](docs/community/en/INSTALLATION.md#on-premise-bare-metal-or-virtual-machine): build, systemd units, nginx and env presets |
| Docker Compose | [Compose installation](docs/community/en/INSTALLATION.md#docker-compose): ports, health checks, transport configuration and persistent data |
| Kubernetes | [Kubernetes installation](docs/community/en/INSTALLATION.md#kubernetes): Helm, Secrets and explicit StorageClass |
| Configuration files | [Preset catalogue](deploy/quickstart/README.md) |

## Documentation

| I want to… | Read |
|---|---|
| Install and send an alert | [Setup](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/SETUP.md) |
| Configure channels, schedules and routing | [Configuration](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/CONFIGURATION.md) |
| Understand grouping and escalation | [Alert processing](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/ALERT_PROCESSING.md) |
| Automate through the API | [API reference](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/API.md) · [OpenAPI](https://github.com/nixys/nxs-anomaly/blob/main/docs/openapi.json) |
| Deploy on Kubernetes | [Helm chart](https://github.com/nixys/nxs-anomaly/blob/main/deploy/helm/nxs-anomaly/README.md) |
| Secure and recover the service | [Security profile](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/SECURITY_PROFILE.md) · [Backup and restore](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/BACKUP_RESTORE.md) |
| Manage configuration with Terraform | [Provider README](https://github.com/nixys/terraform-provider-nxs-anomaly) |

### Troubleshooting

| Symptom | Check |
|---|---|
| Host port already occupied | Change `compose.env` or port-forward/nginx settings; update browser and curl URLs |
| API/worker cannot connect to PostgreSQL | Check DSN, readiness and the password used when the volume was initialized |
| PostgreSQL PVC Pending | Inspect `kubectl -n "$NAMESPACE" get pvc` and the selected StorageClass |
| Login works, Create/Acknowledge returns `cross-origin request rejected` | Use the frontend from the same release; nginx must preserve `Host $http_host` on nonstandard ports |
| Terraform returns 401 | The API process and Terraform must use the same API key; bootstrap password is different |
| Alert accepted, no notification | Check roster, route/chain, user target and transport configuration on the worker |
| Updated token has no effect | Recreate Compose services or update the Secret and restart API/worker |
| Setup complete, Readiness blocked | Read the specific blocker; backup readiness is separate from delivery |
| `ImagePullBackOff` | Verify version, architecture and access to GHCR |

Include the release version, deployment method, failing step, status code and
redacted logs in a bug report. Do not include API keys, bot tokens, passwords,
Kubernetes Secrets or Terraform state.

## Community and feedback

Please follow our [Code of Conduct](CODE_OF_CONDUCT.md). It describes expected
behavior and how to report a concern privately.

Community support is best effort. Start with [GitHub issues](https://github.com/nixys/nxs-anomaly/issues)
for bugs, questions and feature requests. Include your version, installation method,
what you expected and what happened. Feedback on your first installation is especially useful.

- **Found a blocker?** Tell us which step prevented your first notification.
- **Using Community?** Share your monitoring source, delivery channel and what would improve your workflow.
- **Want to follow progress?** Use GitHub **Watch → Custom → Releases** and browse
  [release notes](https://github.com/nixys/nxs-anomaly/releases) and
  [open issues](https://github.com/nixys/nxs-anomaly/issues).

Report vulnerabilities privately using [SECURITY.md](SECURITY.md).
For direct contact: [@Peter_Rukin](https://t.me/Peter_Rukin) or
[p.rukin@nixys.io](mailto:p.rukin@nixys.io). Maintained by [Nixys](https://nixys.io/).

## Roadmap

- **Community:** propose improvements to installation, integrations, delivery,
  on-call workflows and Terraform support through GitHub issues. Describe your
  use case and expected behavior.
- **Enterprise:** discuss SSO, team isolation, analytics and commercial support
  requirements directly with Nixys. Availability depends on edition and configuration.

## Feedback

For support and feedback, contact:

- Telegram: [@Peter_Rukin](https://t.me/Peter_Rukin)
- Email: [p.rukin@nixys.io](mailto:p.rukin@nixys.io)
- Company site: [Nixys contacts](https://nixys.io/contacts/)

## Contributing

Bug reports, documentation improvements and pull requests are welcome.
For a substantial change, open an issue first to discuss the use case and approach.
[CONTRIBUTING.md](CONTRIBUTING.md)
explains the commit format and required checks.

## License

nxs-anomaly Community is released under the [Apache License 2.0](LICENSE).
