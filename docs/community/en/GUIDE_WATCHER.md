# Elasticsearch Watcher

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md). [Set up Elasticsearch](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/installing-elasticsearch).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `elasticsearch`, assign the default chain and copy its routing key.
2. Add a webhook action to the watch. Set method to POST, split the endpoint into scheme, host, port and path, and send a JSON body with `watch_id` from ctx.watch_id. Arrange a recovery action with the same identity and status resolved.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

For the sender configuration, adapt this example to the destination host and key. Use this object as a named entry under the watch's actions.

```json
{
  "webhook": {
    "scheme": "https",
    "host": "oncall.example.org",
    "port": 443,
    "method": "post",
    "path": "/integrations/v1/elasticsearch/REPLACE_KEY",
    "headers": {
      "Content-Type": "application/json"
    },
    "body": "{\"watch_id\":\"{{ctx.watch_id}}\",\"execution_time\":\"{{ctx.execution_time}}\",\"status\":\"firing\"}"
  }
}
```

Test request to the nxs-anomaly API

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"watch_id":"checkout-latency","status":"firing","metadata":{"severity":"warning","team":"checkout"}}'
```

## Verify the alert lifecycle

Execute the watch on test data and inspect its action result. Then exercise recovery. If alerts from two watches merge, inspect their `watch_id` and the integration's grouping policy. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"watch_id":"checkout-latency","status":"resolved","metadata":{"severity":"warning","team":"checkout"}}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
