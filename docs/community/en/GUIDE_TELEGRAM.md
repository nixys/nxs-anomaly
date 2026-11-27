# Telegram bot notifications

## Before you begin

Create a bot with BotFather. Have the test recipient start the bot, obtain their numeric Telegram ID and set `NXS_ANOMALY_TELEGRAM_BOT_TOKEN` in the application secret. For a shared chat add the bot and record the chat ID. [Installation options](INSTALLATION.md). [BotFather and bot setup](https://core.telegram.org/bots/features#creating-a-new-bot).

## Connect and configure

1. Set `telegram_id` on the user and enable telegram in notification_targets.
2. For interactive messages, set `NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET` and register the public HTTPS callback with setWebhook using that secret.
3. Link the team's shared chat as a Telegram ChatOps channel.

```json
{"telegram_id":"123456789","notification_targets":["telegram"]}
```

To exercise the real adapter as an administrator, set API, API_KEY and USER_ID in your shell and run:

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"telegram"}'
```

## Operational check

Set API to the installation HTTPS URL. For a team chat set both name and `external_id` on its ChatOps channel to the numeric chat ID. After a token change register the webhook again and check `pending_update_count` and last_error_message.

```bash
curl --fail-with-body -X POST "https://api.telegram.org/bot$NXS_ANOMALY_TELEGRAM_BOT_TOKEN/setWebhook" \
  --data-urlencode "url=$API/integrations/v1/chatops/telegram" \
  --data-urlencode "secret_token=$NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET"
curl --fail-with-body "https://api.telegram.org/bot$NXS_ANOMALY_TELEGRAM_BOT_TOKEN/getWebhookInfo"
```

## Verify and troubleshoot

Send a personal test notification, then acknowledge a test group with the bot button. For missing messages check the chat ID and whether the user started the bot. For silent buttons inspect getWebhookInfo and the webhook secret after rotation.

[Configuration and setWebhook commands](CONFIGURATION.md#adding-a-telegram-bot) · [Integration catalogue](INTEGRATIONS.md)
