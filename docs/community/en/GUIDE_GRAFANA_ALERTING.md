# Grafana Alerting

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md). [Configure Webhook contact points](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/manage-contact-points/integrations/webhook-notifier/).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `grafana-alerting`, assign the default chain and copy its routing key.
2. Create a Webhook contact point in Grafana Alerting, select POST and paste the route below. Keep the standard webhook body and enable recovery notifications. Assign the contact point to the test rule or its matching notification policy.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/grafana-alerting/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"receiver":"nxs","alerts":[{"status":"firing","fingerprint":"grafana-test","labels":{"alertname":"CheckoutLatency"},"annotations":{"summary":"Checkout latency"}}]}'
```

## Verify the alert lifecycle

Use Test on the contact point, then fire and recover a real test rule. Inspect alerts[] and fingerprint if recovery creates another group. An empty alerts array is rejected. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/grafana-alerting/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"receiver":"nxs","alerts":[{"status":"resolved","fingerprint":"grafana-test","labels":{"alertname":"CheckoutLatency"},"annotations":{"summary":"Checkout latency"}}]}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
