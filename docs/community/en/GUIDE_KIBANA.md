# Kibana Rules

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md). [Set up Kibana](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-kibana).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `elasticsearch`, assign the default chain and copy its routing key.
2. Create a Webhook connector, select POST with JSON and use the endpoint below. Add it to the rule for both active and recovered actions. Substitute rule.id, rule.name and alert.id from the rule context. Set status to recovered in the recovery action.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

For the sender configuration, adapt this example to the destination host and key.

```json
{"rule":{"id":"{{rule.id}}","name":"{{rule.name}}"},"alert":{"id":"{{alert.id}}"},"status":"{{alert.actionGroup}}","message":"{{context.message}}"}
```

Test request to the nxs-anomaly API

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"rule":{"id":"checkout","name":"Checkout"},"alert":{"id":"latency","actionGroup":"active"},"message":"High latency"}'
```

## Verify the alert lifecycle

Trigger a test instance and recover it. Compare rule.id and alert.id in both requests if the incident stays open. Check the connector execution log when no request reaches nxs-anomaly. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"rule":{"id":"checkout","name":"Checkout"},"alert":{"id":"latency","actionGroup":"recovered"},"message":"High latency"}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
