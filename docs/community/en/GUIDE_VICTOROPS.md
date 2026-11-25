# VictorOps / Splunk On-Call

## Prepare the installation

You need a running nxs-anomaly API and worker, editor access, a test recipient and access to configure the sender. From the sender's network check the public HTTPS hostname and certificate. [Install nxs-anomaly](INSTALLATION.md).

## Connect the sender

1. In nxs-anomaly create an escalation chain that notifies the test user. In Integrations create a test integration of type `victorops`, assign the default chain and copy its routing key.
2. Set the destination of the VictorOps-format sender to the URL below. Map the monitored object to `entity_id` and the event type to message_type. Configure the sender's recovery action to reuse that object ID.
3. Set API to the base URL of your installation and KEY to the routing key in the test shell. Run the request below from the network where the sender runs.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/victorops/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"message_type":"CRITICAL","entity_id":"checkout-test","entity_display_name":"Checkout latency","state_message":"Test notification"}'
```

## Verify the alert lifecycle

Send ACKNOWLEDGEMENT followed by RECOVERY using the original entity_id. Check that the active group is acknowledged and then resolved. A successful request returns HTTP 200. In the group timeline confirm the default chain ran and compare the recipient's actual notification with its delivery attempt.

After confirming delivery, close the test incident with the matching recovery request.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/victorops/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"message_type":"RECOVERY","entity_id":"checkout-test","entity_display_name":"Checkout latency","state_message":"Test notification"}'
```

## Operation and troubleshooting

For an unexpected group or missing delivery, compare the source payload and labels with the route preview. For 404 check the full route and active key. For 503 respect Retry-After. When rotating the routing key, update the sender immediately and run a fresh test. [Routing and notification configuration](CONFIGURATION.md) · [Source field mapping](ALERT_PROCESSING.md) · [HTTP responses](API.md).

[Integration catalogue](INTEGRATIONS.md)
