# Integration catalogue

## Receiving alerts

Create an integration, select its escalation chain and copy the keyed URL into the sender configuration. Choose the handler that matches the sender's payload. Each route below accepts HTTP POST. [Configure routes](CONFIGURATION.md#routes) · [Ingest API](API.md#alert-ingest).

| Sender or format | Destination |
|---|---|
| [Generic JSON webhook](GUIDE_WEBHOOK.md) | `/integrations/v1/webhook/{key}`. Reuse the same `nxs_event_id` when retrying an event. |
| [Prometheus Alertmanager](GUIDE_ALERTMANAGER.md) | `/integrations/v1/alertmanager/{key}` for an Alertmanager webhook receiver |
| [Grafana Alerting](GUIDE_GRAFANA_ALERTING.md) | `/integrations/v1/grafana-alerting/{key}` for a Webhook contact point |
| [PagerDuty Events v2](GUIDE_PAGERDUTY.md) | `/integrations/v1/pagerduty/{key}` for Events v2 payloads |
| [VictorOps / Splunk On-Call](GUIDE_VICTOROPS.md) | `/integrations/v1/victorops/{key}` |
| [OpenSearch Alerting](GUIDE_OPENSEARCH.md) | `/integrations/v1/opensearch/{key}` for a channel message template |
| [Kibana Rules](GUIDE_KIBANA.md) · [Elasticsearch Watcher](GUIDE_WATCHER.md) | `/integrations/v1/elasticsearch/{key}` |
| [Legacy NXS pool](GUIDE_LEGACY.md) | `/v2/alert/pool`. Pass the key in the `X-Auth-Key` header. |
| [OpenCVE](OPENCVE.md) | Set the adapter URL in OpenCVE. The adapter translates a batch of CVEs into individual generic webhook events. [Send OpenCVE notifications to Telegram](OPENCVE.md). |

## Delivering notifications and taking action

| Destination | How to connect |
|---|---|
| [Telegram](GUIDE_TELEGRAM.md) | Configure a bot token to deliver to a person or shared chat. Register a webhook for commands and buttons. [Add a Telegram bot](CONFIGURATION.md#adding-a-telegram-bot). |
| [Slack](GUIDE_SLACK.md) · [Mattermost](GUIDE_MATTERMOST.md) | Set the chat's incoming webhook for delivery. Configure callbacks and platform-specific secrets for commands and buttons. [ChatOps](CONFIGURATION.md#8-chatops). |
| [Email delivery over SMTP](GUIDE_SMTP.md) | Connect an SMTP server and add the `email` channel to the user's notification policy. [SMTP setup](CONFIGURATION.md#email-smtp). |
| [Voice calls through Asterisk AMI](GUIDE_ASTERISK.md) | Configure Asterisk AMI and its dialplan. The `call` channel calls the recipient's phone number. |
| [Mobile pairing and push relay](GUIDE_MOBILE.md) | Pair a device to receive events over SSE. For push delivery, set `NXS_ANOMALY_MOBILE_PUSH_URL` to your push relay. [Mobile client](UI.md). |
| [Outbound HTTP webhook](GUIDE_OUTBOUND.md) | Add a `TRIGGER_WEBHOOK` step to POST an Alertmanager-format payload. Add authentication headers if required. [Escalation steps](CONFIGURATION.md#every-step-type). |
| [Redmine issue creation](GUIDE_REDMINE.md) · [Generic issue tracker endpoint](GUIDE_ISSUES.md) | A `CREATE_ISSUE` step opens an issue in a Redmine project. The generic handler posts JSON containing `subject` and `body` to the configured URL. Trackers with a different schema need an adapter. |
| [Forward terminal delivery failures](GUIDE_DEAD_LETTER.md) | Receive an event at a separate endpoint when delivery retries are exhausted. |

Send a test alert and inspect its timeline and delivery attempts to verify the selected chain, channel and provider response. [Verify delivery](CONFIGURATION.md#test-alert-and-delivery-attempt).

## Automation and operations

- [Terraform](GUIDE_TERRAFORM.md) · [REST API](GUIDE_REST.md). Manage resources with Terraform or call the API from your own client. [Terraform provider](https://github.com/nixys/terraform-provider-nxs-anomaly) · [API reference](API.md).
- [Prometheus](GUIDE_PROMETHEUS.md) · [Grafana](GUIDE_GRAFANA.md). Scrape service metrics from `/metrics`, load the alerting rules and import the Grafana dashboard. The Helm chart supports ServiceMonitor and PrometheusRule. [Prometheus rules](ALERTING_RULES.md) · [Grafana dashboard](../../grafana-dashboard.json).
- [Export traces over OTLP/HTTP](GUIDE_OTEL.md). Export API and worker traces to an OTLP/HTTP collector. [Configure tracing](TRACING.md).
- [PostgreSQL](GUIDE_POSTGRES.md) · [Kubernetes / Helm](GUIDE_KUBERNETES.md) · [External Secrets Operator](GUIDE_ESO.md) · [Vault Secrets Operator](GUIDE_VSO.md) · [Ingress](GUIDE_INGRESS.md) · [Istio](GUIDE_ISTIO.md) · [Gateway API](GUIDE_GATEWAY.md). The Helm chart supports an external database, secret delivery through External Secrets Operator or Vault Secrets Operator, and routing through Ingress, Istio or Gateway API. [Deployment](DEPLOY.md) · [Helm settings](../../../deploy/helm/nxs-anomaly/README.md).
- [Outbound delivery proxy](GUIDE_PROXY.md) · [Trust a private TLS certificate authority](GUIDE_CA.md). Configure outbound delivery through a corporate proxy and configure trusted certificates. [Network configuration](PROXY.md).
