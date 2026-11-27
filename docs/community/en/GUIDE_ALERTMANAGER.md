# Prometheus Alertmanager

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the reachable HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md). [Alertmanager overview](https://prometheus.io/docs/alerting/latest/alertmanager/).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `alertmanager`, assign the default chain and copy its routing key.
2. Add a receiver named nxs to alertmanager.yml with `webhook_configs`, url set to the integration URL and `send_resolved` enabled. Add a child route matching your test alert and pointing to nxs. Run `amtool check-config alertmanager.yml`, then reload Alertmanager. [Alertmanager configuration](https://prometheus.io/docs/alerting/latest/configuration/).
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

For the sender configuration, adapt this example to the destination host and key. This minimal configuration routes all alerts to the test receiver. On an existing installation merge the named receiver and a test-only child route into the existing routing tree.

```yaml
route:
  receiver: nxs
receivers:
  - name: nxs
    webhook_configs:
      - url: https://oncall.example.org/integrations/v1/alertmanager/REPLACE_KEY
        send_resolved: true
```

Test request to the nxs-anomaly API

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"version":"4","receiver":"nxs","alerts":[{"status":"firing","labels":{"alertname":"CheckoutTest","service":"checkout"},"fingerprint":"checkout-test"}]}'
```

## Verify the alert lifecycle

Trigger a matching rule in Prometheus. Wait for it to reach `firing` and for Alertmanager to send the first notification, then clear the condition. An alert that resolves during `group_wait` can produce no notification. Check that its fingerprint closes the same group. If the direct POST works but the rule never arrives, inspect routing order, silences and inhibition in Alertmanager. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"version":"4","receiver":"nxs","alerts":[{"status":"resolved","labels":{"alertname":"CheckoutTest","service":"checkout"},"fingerprint":"checkout-test"}]}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
