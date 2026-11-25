# Slack notifications and commands

## Before you begin

Install a Slack app in the workspace, enable incoming webhooks and create one for the destination channel. Copy the channel ID and the app signing secret. The public HTTPS API must be reachable from Slack. [Installation options](INSTALLATION.md). [Create and install the incoming webhook](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/).

## Connect and configure

1. Create a Slack ChatOps channel with the fields below, its team and external channel ID.
2. Set `NXS_ANOMALY_SLACK_SIGNING_SECRET`, register each slash command at /integrations/v1/chatops/slack and set the interactivity request URL to /integrations/v1/chatops/slack/interactive on the same host.
3. Map the user's `slack_id` for attribution.

```json
{
  "platform": "slack",
  "name": "checkout-alerts",
  "external_id": "C01234567",
  "webhook_url": "https://hooks.slack.com/services/REPLACE",
  "notifications_enabled": true,
  "commands_enabled": true,
  "team_id": "REPLACE_TEAM_ID"
}
```

Create the channel via POST /api/v1/chatops/channels or the ChatOps screen. Add a recipient to the linked team. Assign a NOTIFY_USER or NOTIFY_TEAM step to the integration's chain so that at least one team member is paged.

## Verify and troubleshoot

Route a test alert to the channel, invoke a registered command and inspect the group timeline for the actor. If posting works but commands fail, check the request URL, server clock and signing secret. Replace a revoked incoming webhook in the channel settings.

[ChatOps callbacks](API.md) · [Integration catalogue](INTEGRATIONS.md)
