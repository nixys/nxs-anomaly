# OpenSearch Alerting

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md). [Set up OpenSearch](https://docs.opensearch.org/latest/install-and-configure/).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `opensearch`, assign the default chain and copy its routing key.
2. Create a custom webhook notification channel using POST and application/json. Add an action to the monitor trigger. Use the body below as the shape and substitute monitor and trigger IDs from the action context. Add a completed-alert action that sends status resolved. [Alerting actions](https://docs.opensearch.org/latest/observing-your-data/alerting/actions/).
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

For the sender configuration, adapt this example to the destination host and key.

```json
{"monitor":{"id":"{{ctx.monitor._id}}","name":"{{ctx.monitor.name}}"},"trigger":{"id":"{{ctx.trigger._id}}","name":"{{ctx.trigger.name}}","severity":"{{ctx.trigger.severity}}"},"status":"firing"}
```

Test request to the nxs-anomaly API

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/opensearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"monitor":{"id":"checkout","name":"Checkout"},"trigger":{"id":"slow","name":"Latency","severity":"2"},"status":"firing"}'
```

## Verify the alert lifecycle

Run the monitor on a test index and clear its condition. Check monitor and trigger labels. For bucket monitors, keep `bucket_keys` stable on recovery and inspect rendered JSON if a batch returns 400. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/opensearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"monitor":{"id":"checkout","name":"Checkout"},"trigger":{"id":"slow","name":"Latency","severity":"2"},"status":"resolved"}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
