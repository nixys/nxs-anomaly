# First steps for an on-call engineer

## Where nxs-anomaly fits

Your monitoring system detects a condition. For example, Prometheus evaluates a latency rule and Alertmanager sends a webhook. nxs-anomaly records the alert, picks the responsible team and pages whoever is on call. The responder acknowledges the group, investigates in the monitoring system and resolves the group when the incident ends.

## Follow one alert

| Component | What it does | What to check |
|---|---|---|
| Integration | Gives the sender a keyed URL and defines grouping and routing | The incoming event carries the expected service and severity |
| API and PostgreSQL | Accept the event and store the alert and group | The group appears in the web interface |
| Schedule and escalation chain | Select a recipient and the next action if nobody acknowledges | The route selects the intended chain and the schedule covers the current time |
| Worker and delivery provider | Execute due steps and send the message | The delivery attempt records a provider response and the recipient receives the alert |

An **alert** is an incoming event. A **group** collects related alerts and carries the response state. Repeated firing events can update an open group. **Acknowledge** tells the team someone has taken responsibility. **Resolve** closes the group.

## Complete your first shift handoff

1. Choose your deployment method and complete [installation](INSTALLATION.md). Visit the web UI and sign in. Check `/health` on the API and confirm `db_ok` is true.
2. Follow the [first alert walkthrough](SETUP.md) on a test installation. For a real page, create a test recipient with an explicit notification target and configure its provider. The default log target writes to the journal.
3. Configure a team, add the recipient, and create a schedule in the team's time zone. Preview who covers the current shift and the next handoff. Create a chain with a `NOTIFY_SCHEDULE` step and assign it as the integration's default chain. [Configuration](CONFIGURATION.md).
4. Send a firing event with a stable incident key using the [webhook example](GUIDE_WEBHOOK.md). Open the group, inspect its timeline and confirm the scheduled recipient actually received the message.
5. Acknowledge the group. Send the recovery event from the example with the same grouping key. Verify that the same group closes. Record the group ID, recipient, and delivery time so the next operator can repeat the check.

## Connect your monitoring system

Choose the sender format in the [integration catalogue](INTEGRATIONS.md). Start with one service and one team. Preview routes with representative labels before directing production traffic to the new endpoint. Use the [operations guide](OPERATIONS.md) for shift checks and delivery failures.
