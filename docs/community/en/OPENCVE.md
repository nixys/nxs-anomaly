# Send OpenCVE notifications to Telegram

Use this setup to send platform CVEs to one Telegram chat and database CVEs to another. OpenCVE matches vulnerabilities to subscriptions. The adapter turns each webhook batch into alerts. nxs-anomaly selects an escalation chain and sends the Telegram message.

`OpenCVE → adapter POST /webhook → nxs-anomaly integration → chain → Telegram`

## Before you begin

You need an OpenCVE deployment with webhook notifications, access to nxs-anomaly integration settings, Docker for the adapter, and a Telegram bot that can post to the destination chats. OpenCVE must be able to reach the adapter's HTTPS endpoint, and the adapter must be able to reach the nxs-anomaly ingest endpoint.

The **A CVE enters this project** trigger fires when a CVE first matches the project's subscriptions. An older CVE can match when an affected product is added to its record. To limit notifications to recently published CVEs, add a publication date condition in OpenCVE where available. The adapter forwards the events selected by OpenCVE. Alert automations process hourly windows, so the notification time can differ from the publication time. [OpenCVE automations](https://docs.opencve.io/guides/automations/)

## 1. Choose how to split notifications

| Goal | `OPENCVE_ROUTING` | Value of the `subscription` label |
| --- | --- | --- |
| One project per chat or team | `project` (default) | Project name, such as `platform` |
| One project with separate chats for matching subscriptions | `subscription` | Each value in `changes[].cve.subscriptions.raw`, such as `vendor$PRODUCT$product` |

In `subscription` mode, the same CVE can reach several chats. The adapter emits one alert per match, and nxs-anomaly selects the first matching route for each alert. Add the `organization` and `project` labels to a route when you need to distinguish subscriptions with the same name. [OpenCVE webhook format](https://docs.opencve.io/concepts/notifications/#webhook)

## 2. Configure chains in nxs-anomaly

1. Set `NXS_ANOMALY_TELEGRAM_BOT_TOKEN` in the delivery process environment. Add the bot to the destination chats.
2. Create one recipient per chat with an explicit Telegram target. For Bot API delivery, set `"notification_targets": [{"type":"telegram","target":"-1001234567890"}]` on the recipient and replace the example with the actual chat ID.
3. Create a chain with a `NOTIFY_USER` step for each recipient. If responders must acknowledge a CVE, configure a chain with an on-call schedule and escalation.
4. Create a `webhook` integration. Add exact matches on `labels.subscription` for `platform` and `databases` and one default route. Assign a chain to the default route where operators can spot a misspelled subscription or a new project.
5. Copy the integration ingest URL for the adapter. It has the form `https://anomaly.example.com/integrations/v1/webhook/<key>`.

Example route:

```json
{
  "name": "Platform CVEs",
  "match_type": "labels",
  "labels": {"subscription": "platform"},
  "escalation_chain_id": "chn_platform"
}
```

Example `templates.telegram` text:

```text
{{ .title }} · {{ .label_subscription }}
{{ if .label_cvss }}CVSS 3.1 {{ .label_cvss }}
{{ end }}{{ .label_summary }}
{{ .label_cve_url }}
```

See [chains and routes](CONFIGURATION.md) and [notification template variables](ALERT_PROCESSING.md).

## 3. Run the adapter and connect OpenCVE

Build the image from the repository root with `docker build -t nxs-opencve-adapter deploy/opencve`. Create a private `adapter.env` file:

```dotenv
OPENCVE_WEBHOOK_TOKEN=<random token for incoming webhooks>
NXS_ANOMALY_WEBHOOK_URL=https://anomaly.example.com/integrations/v1/webhook/<key>
OPENCVE_ROUTING=project
```

If the integration has a `webhook_secret`, set `NXS_ANOMALY_WEBHOOK_SECRET` on the adapter to the resolved secret value. The adapter signs the body with a timestamp. With the production profile, configure the integration itself using an `env:VARIABLE_NAME` reference. [Webhook signature settings](API.md)

```sh
docker run -d --name opencve-adapter --restart unless-stopped \
  --env-file adapter.env -p 127.0.0.1:18081:8080 \
  -v opencve-adapter-data:/data nxs-opencve-adapter
```

Expose `/webhook` through an HTTPS proxy. In the OpenCVE project, create a Webhook notification with that URL and an `X-OpenCVE-Token` header matching `OPENCVE_WEBHOOK_TOKEN`. Select that notification in the project's alert automation under **Send a notification**. The adapter uses the webhook token for this flow and requires no OpenCVE UI or API credentials. [Configure the OpenCVE webhook](https://docs.opencve.io/concepts/notifications/#webhook)

## 4. Verify routing and delivery

Use a test integration and chat. Check adapter readiness with `GET /health`, then send a synthetic batch. Set `OPENCVE_WEBHOOK_TOKEN` in the current shell and substitute your test project name.

```sh
curl --fail-with-body http://127.0.0.1:18081/webhook \
  -H "X-OpenCVE-Token: ${OPENCVE_WEBHOOK_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{
    "organization": "demo",
    "project": "platform",
    "changes": [
      {"cve": {"cve_id": "CVE-2099-0001", "description": "Delivery test", "cvss31": 8.1,
               "subscriptions": {"raw": ["platform"]}}}
    ]
  }'
```

Expect `{"accepted":1,"duplicates":0}` on the first run and `{"accepted":0,"duplicates":1}` when you replay the batch on the same volume. Open the group in nxs-anomaly and check the `subscription` label, selected chain and delivery attempt. The adapter response confirms ingest by nxs-anomaly. Look for a separate `delivered` attempt and a message in the destination chat to confirm delivery. [Check delivery](CONFIGURATION.md#read-the-result)

## Troubleshoot missing notifications

| Symptom | What to check |
| --- | --- |
| Adapter returns `401` | Check the `X-OpenCVE-Token` header and its value in OpenCVE. |
| Adapter returns `400` | Check `organization`, `project`, `changes[]` and each `cve_id`. In `subscription` mode, each CVE needs a `subscriptions.raw` array. |
| Adapter returns `413` | The request body exceeds the 1 MiB limit. |
| Adapter returns `502` | Check nxs-anomaly availability, integration URL and key, HMAC secret, and clock synchronization. Replay the original batch after fixing the problem. |
| Alert takes the default route | Check the original labels and route order. Pipeline labels are added after route selection. |
| Alert exists but no message arrives | Check that the chain queued a notification, then read the delivery attempt. Verify the bot token, chat ID and posting permissions. |

## Replay and persistent state

Run one adapter instance with a persistent `/data` volume per integration. The adapter records successful ingests by organization, project, subscription and CVE. On a partial failure, replay skips recorded items. Entries remain until you remove the volume, so later updates for the same CVE and subscription are also skipped.

Upgrade nxs-anomaly before deploying this adapter version. If a response is lost before the SQLite write, the adapter sends the same event ID again. nxs-anomaly acknowledges it as a duplicate while the original alert is retained, including after its group closes. Alerts accepted before the server upgrade have no event ID. Keep the SQLite volume when upgrading.

### Investigate a failed batch

`/health` checks the HTTP process. Send a test batch to verify downstream connectivity. In subscription mode an empty `subscriptions.raw` produces zero alerts for that CVE. An integration response of `202 dropped_by_pipeline` becomes an adapter `502`. Inspect drop rules before replaying or the batch will stop at the same item.

Within a batch, the adapter forwards items sequentially with a ten-second network timeout per call. Up to eight batches can forward concurrently. Additional batches receive `503` and can be retried. `/health` continues to answer while forwarding is in progress. Account for the number of matches when sizing proxy timeouts. SQLite records accepted and acknowledged duplicate keys. Replay the original POST with the same token and persistent volume, and verify actual OpenCVE-to-chat delivery during commissioning.
