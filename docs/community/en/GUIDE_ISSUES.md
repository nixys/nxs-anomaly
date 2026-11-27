# Generic issue tracker endpoint

## Before you begin

Provide an HTTPS endpoint or adapter that accepts a POST with subject and body and creates a ticket in your tracker. Configure bearer authentication if the endpoint requires it. [Installation options](INSTALLATION.md).

## Connect and configure

1. Add a CREATE_ISSUE step with `tracker_type` generic.
2. Use the exact creation endpoint.
3. Store the raw bearer token in the environment variable named by token_env.
4. Bind the test chain to the integration.

```json
{"kind":"CREATE_ISSUE","tracker_type":"generic","url":"https://tickets.example.org/intake","token_env":"TRACKER_API_TOKEN"}
```

## Verify and troubleshoot

Trigger the step and verify both the adapter's HTTP response and the created ticket. A 2xx signals acceptance. For 400 inspect the adapter's mapping to the tracker's schema. Check for an existing ticket before manually retrying a timeout.

[Escalation and delivery configuration](CONFIGURATION.md) · [Integration catalogue](INTEGRATIONS.md)
