# Generic JSON webhook

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `webhook`, assign the default chain and copy its routing key.
2. In the sender, map the alert title, lifecycle status and service labels to the JSON fields below. Use a stable `dedupe_key` for the incident and a new `nxs_event_id` for each distinct event. The example uses a test integration without a webhook_secret. For signed delivery, configure the shared secret and signature headers on both ends as described in the API reference.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/webhook/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"title":"API latency","status":"firing","dedupe_key":"checkout-test","nxs_event_id":"checkout-test-01","labels":{"service":"checkout"}}'
```

## Verify the alert lifecycle

Resend the same event ID and check the duplicate response. To close the group, send status resolved with a new event ID and the original dedupe_key. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/webhook/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"title":"API latency","status":"resolved","dedupe_key":"checkout-test","nxs_event_id":"checkout-test-02","labels":{"service":"checkout"}}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
