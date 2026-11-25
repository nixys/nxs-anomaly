# PagerDuty Events v2

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `pagerduty`, assign the default chain and copy its routing key.
2. Configure an Events v2 producer that supports a custom destination. Set its enqueue URL to the endpoint below. The nxs-anomaly routing key belongs in the URL. Retain the `dedup_key` returned from trigger if the sender did not supply one.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/pagerduty/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"event_action":"trigger","dedup_key":"checkout-test","payload":{"summary":"Checkout latency","severity":"warning","source":"checkout"}}'
```

## Verify the alert lifecycle

Send acknowledge, then resolve with the trigger's `dedup_key` in `event_action` requests. Check both transitions in the same group. A missing key on either follow-up yields a validation error. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/pagerduty/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"event_action":"resolve","dedup_key":"checkout-test"}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
