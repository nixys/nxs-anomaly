# Outbound HTTP webhook

## Before you begin

Provide an HTTPS receiver that accepts Alertmanager-format JSON. Decide how it authenticates the worker. If the service is private, configure the permitted destination in the outbound policy and verify DNS and TLS from the worker network. [Installation options](INSTALLATION.md).

## Connect and configure

1. Add the following step to a test escalation chain.
2. Store the complete Authorization header value in the referenced environment secret, attach the chain to the integration and send a matching alert.

```json
{"kind":"TRIGGER_WEBHOOK","webhook_url":"https://hooks.example.org/alerts","headers":{"Authorization":"env:NXS_ANOMALY_GATEWAY_AUTH"}}
```

## Verify and troubleshoot

Compare the receiver's body with the alert in the group timeline and check the HTTP result in delivery attempts. On 401 check the header reference. On `blocked_destination` inspect the outbound allowlist. Make the receiver tolerate duplicate delivery when a response is lost.

[Escalation and delivery configuration](CONFIGURATION.md) · [Integration catalogue](INTEGRATIONS.md)
