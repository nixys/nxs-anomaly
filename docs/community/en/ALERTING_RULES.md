# Prometheus Alerting Rules

*Русская версия: [ALERTING_RULES.md](../ru/ALERTING_RULES.md)*

Example alerting rules for monitoring nxs-anomaly. Adapt the thresholds to your
own load.

## rules.yml

```yaml
groups:
  - name: nxs-anomaly
    interval: 60s
    rules:

      # Ingest errors, by source.
      - alert: IngestErrorsHigh
        expr: >
          sum by (source) (
            rate(nxs_anomaly_ingest_errors_total[5m])
          ) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High ingest error rate from {{ $labels.source }}"
          description: >
            {{ printf "%.2f" $value }} errors/s from source {{ $labels.source }}.
            Check integration key validity and payload format.

      # Notifications keep failing.
      - alert: NotificationFailuresHigh
        expr: >
          rate(nxs_anomaly_notifications_failed_total[10m]) > 0.05
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "High notification failure rate on {{ $labels.instance }}"
          description: >
            {{ printf "%.2f" $value }} permanent failures/s. Check delivery
            adapter credentials (Telegram, SMTP, Asterisk).

      # Delivery is slow — the retry queue is large.
      - alert: NotificationRetryQueueDeep
        expr: nxs_anomaly_notifications_retry_queue_depth > 200
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Notification retry queue backlog on {{ $labels.instance }}"
          description: >
            {{ $value }} notifications in retry queue. Delivery adapters may
            be slow or unreachable.

      # No worker cycle for N minutes.
      # NOTE: this compares against the timestamp of the last cycle, NOT its
      # duration. An earlier version subtracted duration_seconds (0.05s, say)
      # from time() and therefore fired permanently. The metric
      # *_last_cycle_timestamp_seconds exists precisely for stall detection.
      - alert: WorkerCycleStuck
        expr: >
          time() - nxs_anomaly_worker_last_cycle_timestamp_seconds > 120
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Worker cycle not completing on {{ $labels.instance }}"
          description: >
            Last worker cycle completed {{ $value | humanizeDuration }} ago.
            Escalations and notifications may be delayed. Check the worker's
            /ready endpoint and logs.

      # The database is unreachable from the process (worker or API): the ping
      # fails. This is the root cause rather than a symptom — a stuck cycle or a
      # growing backlog are the symptoms.
      - alert: DatabaseUnavailable
        expr: nxs_anomaly_db_up == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Database unreachable from {{ $labels.instance }}"
          description: >
            The last DB ping from this process failed. Ingest, escalation and
            delivery all stop until the database is reachable again.

      # The escalation backlog is ageing: the worker cannot keep groups moving.
      - alert: EscalationBacklogAge
        expr: nxs_anomaly_oldest_due_escalation_age_seconds > 120
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Escalation backlog aging on {{ $labels.instance }}"
          description: >
            Oldest due alert group is {{ $value | humanizeDuration }} past its
            next_run_at. Worker is behind: check it is running and DB is healthy.

      # The delivery backlog is ageing: notifications are stuck in the queue.
      - alert: DeliveryBacklogAge
        expr: nxs_anomaly_oldest_pending_delivery_age_seconds > 120
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Delivery backlog aging on {{ $labels.instance }}"
          description: >
            Oldest delivery_scheduled notification is {{ $value | humanizeDuration }}
            old. Delivery is falling behind ingest.

      # Claims are being reclaimed: workers are dying mid-delivery, or the
      # claim timeout is set too aggressively.
      - alert: StaleClaimsReclaimed
        expr: increase(nxs_anomaly_stale_claims_reclaimed_total[15m]) > 0
        for: 15m
        labels:
          severity: warning
        annotations:
          summary: "Stale delivery claims being reclaimed on {{ $labels.instance }}"
          description: >
            {{ $value }} notifications were reclaimed from a stale 'delivering'/
            'retrying' claim. A non-zero rate means workers crash mid-delivery or
            the claim timeout is too short for the provider latency.

      # Many open alert groups — possibly an incident storm.
      - alert: AlertGroupsBurst
        expr: nxs_anomaly_alert_groups_open > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High number of open alert groups on {{ $labels.instance }}"
          description: >
            {{ $value }} open groups. This may indicate an alert storm.

      # Delivery errors per provider — this is what identifies a broken channel.
      - alert: DeliveryProviderErrors
        expr: >
          sum by (provider) (
            rate(nxs_anomaly_delivery_errors_by_provider_total[10m])
          ) > 0.02
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Delivery errors on {{ $labels.provider }}"
          description: >
            {{ printf "%.3f" $value }} errors/s on provider {{ $labels.provider }}.

      # The worker is behind: cycles are skipped because the previous one is still running.
      - alert: WorkerCyclesSkipped
        expr: rate(nxs_anomaly_worker_cycles_skipped_total[5m]) > 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Worker cycles being skipped on {{ $labels.instance }}"
          description: >
            The worker is behind the load — a cycle starts while the previous
            one is still running. Raise the concurrency or the poll interval, or
            move the worker into separate replicas.

      # A delivery channel has degraded in latency (p95), possibly without errors.
      - alert: DeliveryLatencyHigh
        expr: >
          histogram_quantile(0.95,
            sum by (channel, le) (
              rate(nxs_anomaly_notification_delivery_duration_seconds_bucket[10m])
            )
          ) > 3
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Slow delivery on channel {{ $labels.channel }}"
          description: >
            p95 delivery latency on channel {{ $labels.channel }} =
            {{ printf "%.1f" $value }}s. The provider is alive but slow.

      # Notifications are being lost for good (dead letter).
      - alert: DeadLetterEvents
        expr: >
          sum by (channel) (
            rate(nxs_anomaly_dead_letter_events_total[15m])
          ) > 0
        for: 15m
        labels:
          severity: critical
        annotations:
          summary: "Dead-letter notifications on channel {{ $labels.channel }}"
          description: >
            Notifications on channel {{ $labels.channel }} exhausted their
            retries and became permanently failed. Check the provider and the
            dead-letter webhook.
```

A ready-to-apply `PrometheusRule` manifest with all of these rules is in
[`prometheus-rules.yaml`](../../prometheus-rules.yaml); an importable Grafana
dashboard is in [`grafana-dashboard.json`](../../grafana-dashboard.json).

## Wiring it into Prometheus

The API and a separate worker (`run-worker`) export metrics independently: the
worker brings up its own telemetry port (`/live`, `/ready`, `/metrics`, `:8081`
by default — see `NXS_ANOMALY_WORKER_ADDR`). Without it, the
`serve --no-scheduler` plus `run-worker` arrangement would lose every delivery
metric: the worker produces them, and the registry used to live only in the API's
HTTP server. Scrape both targets.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: nxs-anomaly-api
    static_configs:
      - targets: ['nxs-anomaly-api:8080']
    metrics_path: /metrics
    scrape_interval: 15s
  - job_name: nxs-anomaly-worker
    static_configs:
      - targets: ['nxs-anomaly-worker:8081']
    metrics_path: /metrics
    scrape_interval: 15s
```

## SLI / SLO

The indicators fixed for the beta. The thresholds are a starting point, to be
calibrated against your load (see [CAPACITY.md](CAPACITY.md)).

| SLI | Definition | PromQL | Target SLO |
|---|---|---|---|
| Ingest availability | the share of ingest webhook requests without a 5xx | `1 - (sum(rate(nxs_anomaly_http_requests_total{handler="webhook",code=~"5.."}[5m])) / sum(rate(nxs_anomaly_http_requests_total{handler="webhook"}[5m])))` | ≥ 99.9% |
| Ingest→first-attempt latency | p95 of the delay from accepting an alert to the first delivery attempt | `histogram_quantile(0.95, sum by (le) (rate(nxs_anomaly_ingest_duration_seconds_bucket[5m]))) + nxs_anomaly_oldest_pending_delivery_age_seconds` | p95 < 10s |
| Successful delivery ratio (per provider) | the share of successful deliveries per channel | `sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) / (sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) + sum by (provider) (rate(nxs_anomaly_delivery_errors_by_provider_total[30m])))` | ≥ 99% |
| Acknowledge API latency | p95 of acknowledge requests | `histogram_quantile(0.95, sum by (le) (rate(nxs_anomaly_http_request_duration_seconds_bucket{handler="api"}[5m])))` | p95 < 2s |

A direct measurement of ingest→first-attempt, without decomposing it into two
metrics, comes from the load harness: it times the gap between ingest and the
first `delivery_attempt` and prints p50/p95/p99. See [CAPACITY.md](CAPACITY.md).

> The ingest SLI's label is `handler="webhook"`. That is the category
> `classifyHandler` (`internal/server/server_internal.go`) assigns to the
> `/integrations/v1/` and `/v2/alert/` paths; there is no `handler="ingest"`
> value, and a query using one silently returns nothing.

### Error budget and burn rate

Every threshold alert above compares an instantaneous rate against a constant.
Such a threshold cannot tell a two-minute spike from a week of slow bleeding: the
spike wakes somebody and passes on its own, while the bleeding never crosses the
threshold and quietly eats the whole SLO. So the `nxs-anomaly.slo` group in
[prometheus-rules.yaml](../../prometheus-rules.yaml) alerts not on errors but on
the **rate at which the error budget is being spent** (Google SRE Workbook,
chapter 5).

The budget is `1 - SLO`. The burn rate is the observed error ratio divided by
the budget. A burn rate of 1 spends a monthly budget in exactly 30 days; 14.4
spends 2% of it in an hour.

| Tier | Burn rate | Windows | Budget exhausted in | severity |
|---|---|---|---|---|
| Fast | 14.4 | 5m **and** 1h | ~2 days | `critical` — wake somebody |
| Slow | 6 | 30m **and** 6h | ~5 days | `warning` — a ticket |

Both windows are required at once: the short one gives a fast trigger and a fast
reset, and the long one keeps a brief blip from waking anybody.

| SLI | SLO | Budget | Fast threshold | Slow threshold | Alerts |
|---|---|---|---|---|---|
| Ingest availability | 99.9% | 0.1% | 1.44% 5xx | 0.6% 5xx | `IngestErrorBudgetBurnFast` / `Slow` |
| Delivery success (per provider) | 99% | 1% | 14.4% errors | 6% errors | `DeliveryErrorBudgetBurnFast` / `Slow` |

The ratios are computed by the recording rules
`nxs_anomaly:ingest_5xx:ratio_rate*` and
`nxs_anomaly:delivery_errors:ratio_rate*`, which double as ready dashboard
panels.

What these rules do **not** catch: with no traffic the ratio is `0/0 = NaN`,
every comparison against NaN is false, and a quiet instance stays quiet instead
of alerting about a division by zero. The flip side is that ingest stopping
altogether is invisible to a burn rate; `WorkerCycleStuck` and
`DatabaseUnavailable` cover that.

`DeliverySuccessRatioLow` is kept deliberately: it catches a dip below 99% over
a 30-minute window, so it fires earlier than the slow tier but without reference
to the budget. The burn-rate alerts answer a different question — "will we make
it to the end of the month" rather than "is it bad right now".

## Grafana dashboard (a quick start)

The main panels:

| Panel | PromQL |
|---|---|
| Ingested alerts/s | `rate(nxs_anomaly_alerts_ingested_total[1m])` |
| Ingest errors/s by source | `sum by(source)(rate(nxs_anomaly_ingest_errors_total[1m]))` |
| Open alert groups | `nxs_anomaly_alert_groups_open` |
| Skipped notifications | `nxs_anomaly_notifications_skipped_total` |
| Schedule coverage | `nxs_anomaly_schedules_degraded` |
| Shifts nobody confirmed | `nxs_anomaly_duty_without_checkin` |
| Sources gone quiet | `nxs_anomaly_sources_silent` |
| Retention deletions by category | `sum by(category)(increase(nxs_anomaly_retention_deleted_total[24h]))` |
| Notification retry queue | `nxs_anomaly_notifications_retry_queue_depth` |
| Delivery errors by provider | `sum by(provider)(rate(nxs_anomaly_delivery_errors_by_provider_total[5m]))` |
| Worker cycle duration | `nxs_anomaly_worker_cycle_duration_seconds` |
| Time since last worker cycle | `time() - nxs_anomaly_worker_last_cycle_timestamp_seconds` |
| DB up | `nxs_anomaly_db_up` |
| Escalation backlog age | `nxs_anomaly_oldest_due_escalation_age_seconds` |
| Delivery backlog age | `nxs_anomaly_oldest_pending_delivery_age_seconds` |
| Delivered/s by provider | `sum by(provider)(rate(nxs_anomaly_notifications_delivered_by_provider_total[5m]))` |
| Per-provider success ratio | `sum by(provider)(rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) / (sum by(provider)(rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) + sum by(provider)(rate(nxs_anomaly_delivery_errors_by_provider_total[30m])))` |
| Worker cycles skipped/s | `rate(nxs_anomaly_worker_cycles_skipped_total[5m])` |
| Delivery latency p95 by channel | `histogram_quantile(0.95, sum by(channel,le)(rate(nxs_anomaly_notification_delivery_duration_seconds_bucket[5m])))` |
| Dead-letters/s by channel | `sum by(channel)(rate(nxs_anomaly_dead_letter_events_total[5m]))` |
| Short-circuited deliveries/s | `sum by(channel)(rate(nxs_anomaly_delivery_short_circuited_total[5m]))` |

There is no universal alert rule for retention: a zero `increase` can mean
either that the sweep has stopped or that there are no rows past the cutoff.
Read it against the horizons you chose and the size of the tables; readiness
warns separately when no policy has been chosen.
| Ingest latency p95 | `histogram_quantile(0.95, rate(nxs_anomaly_ingest_duration_seconds_bucket[5m]))` |
