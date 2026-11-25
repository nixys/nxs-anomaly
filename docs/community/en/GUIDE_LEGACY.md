# Legacy NXS pool

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `webhook`, assign the default chain and copy its routing key.
2. Point the existing nxs-alert sender at /v2/alert/pool. Put the integration key in the X-Auth-Key header. Include both triggerMessage and monitoringURL. Review the legacy channel defaults before connecting a production pool.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

```bash
curl --fail-with-body -i -X POST "$API/v2/alert/pool" \
  -H 'Content-Type: application/json' \
  -H "X-Auth-Key: $KEY" \
  --data-binary '{"triggerMessage":"Checkout test","monitoringURL":"https://monitor.example.org/checkout","alertChannel":"email"}'
```

## Verify the alert lifecycle

Expect HTTP 200 and message success. Inspect the group title from the first message line and the notification targets. The compatibility handler creates a firing alert. Resolve the test group in the UI. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

Check legacy channel selection before switching senders. The handler always adds email. When neither telegram nor call appears, it adds both. Test with a dedicated recipient and close the group in the UI after checking the channel list.

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
