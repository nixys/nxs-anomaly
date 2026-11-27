# Operating an on-call installation

## Start a shift

Check the schedule in the team's time zone, including overrides and the next handoff. Review open and acknowledged groups with the outgoing responder. Agree an owner and next action for each outstanding incident. After a schedule or channel change, run a test alert addressed to that schedule and check receipt on the actual device.

Check both processes. Set API to the service base URL and WORKER to the worker HTTP listener reachable from your network. In Kubernetes you can use port forwarding for internal ports. Inspect db_ok and the version on the API and the HTTP result on the worker.

```bash
curl --fail-with-body "$API/health"
curl --fail-with-body "$WORKER/ready"
```

## Find where a notification stopped

| Observation | Next check |
|---|---|
| The source reports an HTTP error | Check the exact handler path and routing key. On 400 inspect the rendered JSON. On 503 check database readiness and respect Retry-After. |
| The source succeeded but there is no new group | Read the ingest response. A repeat can join an existing group. Check the integration's drop rules, maintenance window and routing preview. |
| A group exists but there is no delivery attempt | Inspect the timeline for the selected chain, wait steps and covered recipient. Check the worker's /ready endpoint and whether recent worker cycles completed. |
| The attempt was skipped | Read the reason. Check enabled targets, provider credentials and outbound policy on the worker. |
| The provider accepted the message but the user heard nothing | Compare the actual address with the recipient's target. Inspect the provider queue or chat history and confirm receipt with the responder. |

Record the group ID, integration, timestamps, channel and provider response before changing settings. Retest one group after the change. [Configuration and delivery checks](CONFIGURATION.md) · [Service metrics](ALERTING_RULES.md).

## Maintain the installation

- After rotating an ingest key, update the sender immediately and check both firing and recovery. For provider credentials, reload the API and worker environment and repeat the delivery test.
- Before upgrading, save the release values and take a database backup. Restore it into an isolated database and check readiness before connecting delivery providers. [Backup and recovery](BACKUP_RESTORE.md) · [Migrations](MIGRATIONS.md).
- After upgrading, verify the version on /health, the worker's readiness, schedule coverage and a complete test notification. Monitor ingest errors, delivery failures and retry queue depth against the usual traffic level. [Deployment](DEPLOY.md).
