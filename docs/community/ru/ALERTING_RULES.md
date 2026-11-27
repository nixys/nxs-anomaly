# Правила оповещения Prometheus

Prometheus считывает метрики API и воркера с `/metrics` и вычисляет правила на
PromQL. Когда правило переходит в состояние `firing`, Prometheus передаёт оповещение
в Alertmanager, который направляет его в нужный канал. См. [обзор Prometheus](https://prometheus.io/docs/introduction/overview/).

В примере ниже указаны начальные пороги. Сравните их с метриками сервиса
в обычные часы и в часы пик. Например, при выборе порога для очереди доставки
ориентируйтесь на время ожидания уведомлений, которое допустимо для дежурных.
Параметр `for` задаёт минимальное время, в течение которого условие должно
выполняться непрерывно для перехода правила в состояние `firing`.

## Пример файла rules.yml

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
          and nxs_anomaly_worker_last_cycle_timestamp_seconds > 0
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

      # A process is gone, not degraded. Every rule above reads metrics the
      # process itself exports, so a dead API or worker silently takes them
      # with it; only the scrape result says so. The job label is the Service
      # name under the chart's ServiceMonitor (<release>-nxs-anomaly-api,
      # -worker) or your job_name in a static config — adjust the regex if you
      # use fullnameOverride.
      - alert: NxsAnomalyTargetDown
        expr: up{job=~".*nxs-anomaly.*"} == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "nxs-anomaly target {{ $labels.job }} down on {{ $labels.instance }}"
          description: >
            Prometheus cannot scrape this nxs-anomaly process. Check the target and the network path from Prometheus. Route this alert around nxs-anomaly.

      # No worker anywhere completes cycles: all are down, crash-looping or not
      # scraped at all. "> 0" matters: an API started with --no-scheduler
      # exports this gauge too, at 0, and would otherwise mask the absence.
      - alert: WorkerAbsent
        expr: absent(nxs_anomaly_worker_last_cycle_timestamp_seconds > 0)
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "No nxs-anomaly worker is completing cycles"
          description: >
            No scraped process reports a completed worker cycle. Check worker availability and scrape discovery. Route this alert around nxs-anomaly.

      # Always firing, by design. It proves the Prometheus → Alertmanager →
      # receiver path works: send it to an external dead-man's switch, which
      # alarms when it STOPS arriving. Never route it into nxs-anomaly.
      - alert: Watchdog
        expr: vector(1)
        labels:
          severity: none
        annotations:
          summary: "Alerting pipeline heartbeat (always firing)"
          description: >
            Send to a dead-man's-switch receiver with a short repeat_interval.
            If heartbeats stop, check rule evaluation, the Alertmanager route and webhook delivery.

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

      # Paging-latency SLI: pages leave the queue late (SLO: p95 < 10 s).
      - alert: PagingLatencyHigh
        expr: >
          histogram_quantile(0.95,
            sum by (channel, le) (
              rate(nxs_anomaly_notification_first_attempt_delay_seconds_bucket[10m])
            )
          ) > 10
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Pages leave late on channel {{ $labels.channel }}"
          description: >
            p95 from queued to first delivery attempt = {{ printf "%.1f" $value }}s.
            The worker is behind: check cycle duration and the delivery backlog.

      # Responder SLI: acknowledge/resolve is slow (SLO: p95 < 2 s).
      - alert: RespondLatencyHigh
        expr: >
          histogram_quantile(0.95,
            sum by (le) (
              rate(nxs_anomaly_http_request_duration_seconds_bucket{handler="respond"}[10m])
            )
          ) > 2
          and sum(rate(nxs_anomaly_http_request_duration_seconds_count{handler="respond"}[10m])) > 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Acknowledge/resolve is slow"
          description: >
            p95 responder action latency = {{ printf "%.1f" $value }}s; the page
            keeps escalating while the responder waits on the button.

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

Готовый к применению манифест `PrometheusRule` со всеми правилами лежит в
[`prometheus-rules.yaml`](../../prometheus-rules.yaml); дашборд Grafana для импорта —
[`grafana-dashboard.json`](../../grafana-dashboard.json).

## Подключение к Prometheus

API и отдельный воркер (`run-worker`) публикуют метрики на разных портах. Если
API запущен с `--no-scheduler`, добавьте в конфигурацию Prometheus обе точки сбора,
чтобы видеть и приём вебхуков, и работу очереди доставки. Воркер по умолчанию
слушает порт `8081`. Адрес задаётся через `NXS_ANOMALY_WORKER_ADDR`.

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

## Мониторинг в обход nxs-anomaly

Настройте в Alertmanager независимый канал, например прямую отправку в Telegram.
Направьте туда оповещения о сбоях самого nxs-anomaly, чтобы дежурные получали их,
даже когда сервис недоступен. Для `Watchdog` укажите внешний сервис, который
оповестит дежурного при прекращении регулярных сигналов.

Добавьте эти маршруты и получателей в действующую конфигурацию Alertmanager.

```yaml
# alertmanager.yml
route:
  routes:
    - matchers: ['alertname="Watchdog"']
      receiver: deadmans-switch
      group_interval: 1m
      repeat_interval: 1m
    - matchers: ['alertname=~"NxsAnomalyTargetDown|WorkerAbsent|DatabaseUnavailable|WorkerCycleStuck"']
      receiver: ops-direct          # Telegram/e-mail/SMS directly, not nxs-anomaly
      continue: false
receivers:
  - name: deadmans-switch
    webhook_configs:
      - url: https://hc-ping.com/<uuid>
  - name: ops-direct
    telegram_configs:
      - chat_id: <id>
        bot_token_file: /etc/alertmanager/telegram-token
```

`Watchdog` подтверждает, что регулярный сигнал доходит через Prometheus и
Alertmanager до внешнего получателя. Чтобы отдельно проверять работу воркера,
задайте `NXS_ANOMALY_WORKER_HEARTBEAT_URL`. После успешного цикла воркер отправляет
`GET` на этот адрес, максимум раз в минуту. Если цикл завершился ошибкой базы
данных, запрос не отправляется.

Установите во внешнем сервисе интервал ожидания не менее пяти минут. При общем
URL для всех реплик оповещение придёт, когда сигналы перестанут поступать от
всех реплик. В Helm-чарте задайте URL через Secret приложения или
`worker.extraEnv`. В URL содержится токен проверки.

## Показатели и цели надёжности (SLI и SLO)

SLI показывает фактическую надёжность сервиса, а SLO задаёт целевое значение.
Например, SLI приёма вебхуков — доля запросов без ответа `5xx`, а SLO —
не менее 99,9% за 30 дней. Окна в PromQL ниже задают интервалы для наблюдения
за метриками. Целевые значения приведены как отправная точка. Для выбора цели
по задержке проверьте, за какое время дежурный должен получить уведомление,
и сравните этот срок с результатами нагрузочного теста. См. [CAPACITY.md](./CAPACITY.md).

| Показатель | Что измеряем | PromQL | Цель |
|---|---|---|---|
| Доступность приёма вебхуков | Доля запросов без ответа `5xx` | `1 - (sum(rate(nxs_anomaly_http_requests_total{handler="webhook",code=~"5.."}[5m])) / sum(rate(nxs_anomaly_http_requests_total{handler="webhook"}[5m])))` | ≥ 99,9% |
| Время до первой попытки доставки | p95 от постановки уведомления в очередь до первого вызова провайдера, по каналам | `histogram_quantile(0.95, sum by (channel, le) (rate(nxs_anomaly_notification_first_attempt_delay_seconds_bucket[10m])))` | p95 < 10 с |
| Успешность доставки | Доля успешных попыток по каждому провайдеру | `sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) / (sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) + sum by (provider) (rate(nxs_anomaly_delivery_errors_by_provider_total[30m])))` | ≥ 99% |
| Время ответа API на действия дежурного | p95 запросов подтверждения и закрытия оповещений | `histogram_quantile(0.95, sum by (le) (rate(nxs_anomaly_http_request_duration_seconds_bucket{handler="respond"}[10m])))` | p95 < 2 с |

p95 — значение, в которое укладываются примерно 95% наблюдений. Чтобы измерить
полный путь от приёма события до первой попытки доставки, запустите нагрузочный
тест из [CAPACITY.md](./CAPACITY.md).

Запросы к `/integrations/v1/` и `/v2/alert/` учитываются с меткой
`handler="webhook"`.

### Бюджет ошибок и скорость его расхода

Для SLO 99,9% бюджет ошибок составляет 0,1% запросов за 30 дней. Скорость
расхода бюджета (*burn rate*) равна наблюдаемой доле ошибок, делённой на
допустимую долю. Например, при 1,44% ответов `5xx` бюджет расходуется в
14,4 раза быстрее допустимого темпа. При постоянном потоке запросов за час
уйдёт около 2% 30-дневного бюджета.

Группа `nxs-anomaly.slo` проверяет долю ошибок за два окна. Длинное окно
показывает, что уже израсходована заметная часть бюджета, а короткое
подтверждает, что ошибки продолжаются. Условие должно выполняться в обоих
окнах в течение времени, заданного в `for`. Такой подход помогает реагировать
на длительное ухудшение, даже если число ошибок в секунду невелико.
См. [Google SRE Workbook, глава Alerting on SLOs](https://sre.google/workbook/alerting-on-slos/).

| Уровень | Скорость расхода | Окна | `for` | Действие |
|---|---|---|---|---|
| Быстрый | > 14,4× | 5 мин и 1 ч | 2 мин | `critical` — уведомить дежурного |
| Устойчивый | > 6× | 30 мин и 6 ч | 15 мин | `warning` — завести задачу |

| Показатель | Бюджет | Быстрый расход | Устойчивый расход | Правила |
|---|---|---|---|---|
| Приём вебхуков | 0,1% | > 1,44% ответов `5xx` | > 0,6% ответов `5xx` | `IngestErrorBudgetBurnFast`, `IngestErrorBudgetBurnSlow` |
| Доставка по провайдеру | 1% | > 14,4% ошибок | > 6% ошибок | `DeliveryErrorBudgetBurnFast`, `DeliveryErrorBudgetBurnSlow` |

Правила записи (`recording rules`) заранее вычисляют доли ошибок для обоих
показателей. Их результаты доступны в рядах `nxs_anomaly:ingest_5xx:ratio_rate*`
и `nxs_anomaly:delivery_errors:ratio_rate*`.

При отсутствии запросов или попыток доставки доля ошибок не определена, поэтому
оповещения о расходе бюджета не срабатывают. Доступность точек сбора, состояние
базы и завершение циклов воркера проверяются отдельными правилами.

`DeliverySuccessRatioLow` предупреждает, если успешность доставки ниже 99%
за 30 минут, а частота попыток выше 0,01 в секунду. Условие должно сохраняться
15 минут.

## Панели Grafana

| Панель | PromQL |
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
| Ingest latency p95 | `histogram_quantile(0.95, rate(nxs_anomaly_ingest_duration_seconds_bucket[5m]))` |

Для проверки очистки данных сопоставьте число удалённых записей со сроком
хранения и размером таблиц. Нулевое значение само по себе не позволяет
определить, выполнялась ли очистка.
