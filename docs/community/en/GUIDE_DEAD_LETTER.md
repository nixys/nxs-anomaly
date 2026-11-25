# Forward terminal delivery failures

## Before you begin

Prepare an HTTPS receiver for notification.failed events. Give operators access to its logs and keep this path independent of the channel being monitored. [Installation options](INSTALLATION.md).

## Connect and configure

1. Set the endpoint below in the worker environment.
2. Configure the receiver to record `notification_id`, `alert_group_id`, channel, `retry_count` and `last_error` and notify the operator.

```dotenv
NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL=https://hooks.example.org/dead-letter
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

## Verify and troubleshoot

In a test chain use a controlled receiver returning HTTP 503. Wait for its retries to exhaust and match the failed delivery ID with the event. Restore the test receiver afterwards. The dead-letter POST is best effort; monitor `dead_letter_delivery_failed` in worker logs.

[Escalation and delivery configuration](CONFIGURATION.md) · [Integration catalogue](INTEGRATIONS.md)
