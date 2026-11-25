# Mattermost notifications and commands

## Before you begin

Create an incoming webhook for the team channel in Mattermost. Obtain its channel ID. Allow Mattermost to POST to the public HTTPS address of nxs-anomaly. [Installation options](INSTALLATION.md). [Enable incoming webhooks](https://developers.mattermost.com/integrate/webhooks/incoming/).

## Connect and configure

1. Create a Mattermost ChatOps channel as below.
2. Set `NXS_ANOMALY_PUBLIC_URL` and `NXS_ANOMALY_MATTERMOST_ACTION_SECRET` for buttons.
3. Register a POST slash command at /integrations/v1/chatops/mattermost/command and put its issued token in NXS_ANOMALY_MATTERMOST_COMMAND_TOKEN.
4. Link the person's mattermost_id.

```json
{
  "platform": "mattermost",
  "name": "checkout-alerts",
  "external_id": "REPLACE_CHANNEL_ID",
  "webhook_url": "https://chat.example.org/hooks/REPLACE",
  "notifications_enabled": true,
  "commands_enabled": true,
  "team_id": "REPLACE_TEAM_ID"
}
```

Create the channel via POST /api/v1/chatops/channels or the ChatOps screen. Add a recipient to the linked team. Assign a NOTIFY_USER or NOTIFY_TEAM step to the integration's chain so that at least one team member is paged.

## Verify and troubleshoot

Send a test alert, press its acknowledge button and run the registered slash command with help. Missing buttons point to the public URL or action secret. A 501 on the command points to its separate token. Verify both after credential rotation.

[Mattermost bot setup](CONFIGURATION.md#adding-a-mattermost-bot) · [Integration catalogue](INTEGRATIONS.md)
