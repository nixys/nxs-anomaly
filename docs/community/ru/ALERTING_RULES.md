# Prometheus Alerting Rules

*In English: [ALERTING_RULES.md](../en/ALERTING_RULES.md)*

Пример правил алертинга для мониторинга нxs-anomaly. Адаптируйте пороги под свою нагрузку.

## rules.yml

```yaml
groups:
  - name: nxs-anomaly
    interval: 60s
    rules:

      # Ошибки ingest по источнику.
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

      # Уведомления постоянно падают.
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

      # Уведомления доставляются медленно — большая очередь retry.
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

      # Worker-цикл не выполнялся последние N минут.
      # ВНИМАНИЕ: сравнивается с timestamp последнего цикла, а НЕ с его
      # длительностью. Прежняя версия вычитала duration_seconds (например 0.05с)
      # из time() и потому горела всегда. Метрика *_last_cycle_timestamp_seconds
      # существует именно для детекта простоя.
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

      # База недоступна из процесса (worker или API): ping не проходит.
      # Это первопричина, а не симптом (stuck-cycle, растущий backlog).
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

      # Backlog эскалаций стареет: worker не успевает продвигать группы.
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

      # Backlog доставки стареет: уведомления зависли в очереди.
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

      # Реклейм чужих claim-ов: worker'ы падают на середине доставки или
      # claim-timeout выставлен слишком агрессивно.
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

      # Много открытых alert groups — возможна инцидентная буря.
      - alert: AlertGroupsBurst
        expr: nxs_anomaly_alert_groups_open > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High number of open alert groups on {{ $labels.instance }}"
          description: >
            {{ $value }} open groups. This may indicate an alert storm.

      # Delivery errors per provider — помогает определить сломанный канал.
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

      # Worker не успевает: циклы пропускаются, потому что предыдущий ещё идёт.
      - alert: WorkerCyclesSkipped
        expr: rate(nxs_anomaly_worker_cycles_skipped_total[5m]) > 0
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Worker cycles being skipped on {{ $labels.instance }}"
          description: >
            Worker не успевает за нагрузкой — цикл стартует, пока предыдущий ещё
            выполняется. Увеличьте concurrency/poll-interval или вынесите worker
            в отдельные реплики.

      # Процесс не упал в деградацию, а пропал. Все правила выше читают метрики,
      # которые экспортирует сам процесс, и мёртвый API или worker молча уносит
      # их с собой — об этом говорит только результат скрейпа. Label job — имя
      # Service под ServiceMonitor чарта (<release>-nxs-anomaly-api, -worker)
      # или ваш job_name в static-конфиге; при fullnameOverride поправьте regex.
      - alert: NxsAnomalyTargetDown
        expr: up{job=~".*nxs-anomaly.*"} == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "nxs-anomaly target {{ $labels.job }} down on {{ $labels.instance }}"
          description: >
            Prometheus не может снять метрики с процесса nxs-anomaly. Если это
            worker — никого не будят. Маршрутизируйте в обход nxs-anomaly.

      # Ни один worker не завершает циклы: все упали, в crash-loop или вообще не
      # скрейпятся. «> 0» обязательно: API с --no-scheduler тоже экспортирует
      # этот gauge, но со значением 0, и без фильтра маскировал бы отсутствие.
      - alert: WorkerAbsent
        expr: absent(nxs_anomaly_worker_last_cycle_timestamp_seconds > 0)
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "No nxs-anomaly worker is completing cycles"
          description: >
            Ни один процесс не сообщает о завершённом цикле worker-а. Эскалация и
            доставка стоят. Маршрутизируйте в обход nxs-anomaly.

      # Горит всегда — так задумано. Доказывает, что путь Prometheus →
      # Alertmanager → receiver жив: отправляйте его во внешний dead-man's switch,
      # который поднимает тревогу, когда алерт ПЕРЕСТАЁТ приходить. Никогда не
      # направляйте его в сам nxs-anomaly.
      - alert: Watchdog
        expr: vector(1)
        labels:
          severity: none
        annotations:
          summary: "Alerting pipeline heartbeat (always firing)"
          description: >
            Receiver — dead-man's switch с коротким repeat_interval. Его молчание
            означает, что лежит Prometheus или Alertmanager.

      # Канал доставки деградировал по латентности (p95), хотя ошибок может не быть.
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
            p95 латентность доставки по каналу {{ $labels.channel }} =
            {{ printf "%.1f" $value }}s. Провайдер жив, но медленный.

      # Уведомления окончательно теряются (dead-letter).
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
            Уведомления по каналу {{ $labels.channel }} исчерпали retry и стали
            permanently failed. Проверьте провайдер и dead-letter webhook.
```

Готовый к применению манифест `PrometheusRule` со всеми правилами лежит в
[`prometheus-rules.yaml`](../../prometheus-rules.yaml); дашборд Grafana для импорта —
[`grafana-dashboard.json`](../../grafana-dashboard.json).

## Подключение к Prometheus

API и отдельный worker (`run-worker`) экспонируют метрики независимо: worker
поднимает собственный телеметрийный порт (`/live`, `/ready`, `/metrics`,
по умолчанию `:8081`, см. `NXS_ANOMALY_WORKER_ADDR`). Без этого схема
`serve --no-scheduler` + `run-worker` теряла бы все delivery-метрики: их
производит worker, а раньше реестр жил только в HTTP-сервере API. Скрейпить надо
оба таргета.

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

Алерт о том, что nxs-anomaly не работает, нельзя доставлять через nxs-anomaly:
он придёт в ту самую систему, которая лежит. Поэтому три правила выше —
`NxsAnomalyTargetDown`, `WorkerAbsent` и `Watchdog` — нужно маршрутизировать
отдельным receiver-ом Alertmanager прямо в независимый канал, а `Watchdog` — во
внешний dead-man's switch:

```yaml
# alertmanager.yml
route:
  routes:
    - matchers: ['alertname="Watchdog"']
      receiver: deadmans-switch
      repeat_interval: 1m
    - matchers: ['alertname=~"NxsAnomalyTargetDown|WorkerAbsent|DatabaseUnavailable|WorkerCycleStuck"']
      receiver: ops-direct          # Telegram/почта/SMS напрямую, не nxs-anomaly
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

`Watchdog` проверяет цепочку Prometheus → Alertmanager, но не сам nxs-anomaly.
Для прямого сигнала от worker-а задайте `NXS_ANOMALY_WORKER_HEARTBEAT_URL`: после
успешного цикла worker делает `GET` на этот адрес, не чаще раза в минуту. Цикл,
упавший на базе, пинга не даёт, поэтому один такой check покрывает worker, его
базу и сеть — без Prometheus. Период проверки во внешнем сервисе ставьте с
запасом: не меньше 5 минут. Пинг уходит от каждой реплики worker-а, так что
check молчит, только когда не работает ни одна.

В Helm-чарте переменную задают через Secret приложения или `worker.extraEnv`;
URL содержит токен check-а, поэтому в логи он не пишется.

## SLI / SLO

Зафиксированные индикаторы для beta. Пороги — стартовые, калибруются под нагрузку
(см. [CAPACITY.md](./CAPACITY.md)).

| SLI | Определение | PromQL | Целевой SLO |
|---|---|---|---|
| Ingest availability | доля webhook-запросов ingest без 5xx | `1 - (sum(rate(nxs_anomaly_http_requests_total{handler="webhook",code=~"5.."}[5m])) / sum(rate(nxs_anomaly_http_requests_total{handler="webhook"}[5m])))` | ≥ 99.9% |
| Ingest→first-attempt latency | p95 задержки от приёма алерта до первой попытки доставки | `histogram_quantile(0.95, sum by (le) (rate(nxs_anomaly_ingest_duration_seconds_bucket[5m]))) + nxs_anomaly_oldest_pending_delivery_age_seconds` | p95 < 10s |
| Successful delivery ratio (per provider) | доля успешных доставок по каналу | `sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) / (sum by (provider) (rate(nxs_anomaly_notifications_delivered_by_provider_total[30m])) + sum by (provider) (rate(nxs_anomaly_delivery_errors_by_provider_total[30m])))` | ≥ 99% |
| Acknowledge API latency | p95 ack-запросов | `histogram_quantile(0.95, sum by (le) (rate(nxs_anomaly_http_request_duration_seconds_bucket{handler="api"}[5m])))` | p95 < 2s |

Прямой замер ingest→first-attempt (без разложения на две метрики) даёт нагрузочный
harness — он засекает время между ingest и первым `delivery_attempt` и печатает
p50/p95/p99. См. [CAPACITY.md](./CAPACITY.md).

> Метка ingest-SLI — `handler="webhook"`. Это категория, которую `classifyHandler`
> (`internal/server/server_internal.go`) присваивает путям `/integrations/v1/` и
> `/v2/alert/`; значения `handler="ingest"` не существует, и запрос с ним молча
> возвращает пустой результат.

### Error budget и burn rate

Все пороговые алерты выше сравнивают мгновенную скорость с константой. Такой порог
не отличает двухминутный всплеск от недели медленного кровотечения: всплеск
разбудит дежурного и пройдёт сам, а кровотечение никогда не перешагнёт порог и
тихо съест SLO целиком. Поэтому группа `nxs-anomaly.slo` в
[prometheus-rules.yaml](../../prometheus-rules.yaml) алертит не на ошибки, а на
**скорость расхода бюджета ошибок** (Google SRE Workbook, гл. 5).

Бюджет = `1 - SLO`. Burn rate = наблюдаемая доля ошибок / бюджет. Burn rate 1
расходует месячный бюджет ровно за 30 дней, 14.4 — 2% бюджета за час.

| Тир | Burn rate | Окна | Бюджет кончится через | severity |
|---|---|---|---|---|
| Fast | 14.4 | 5m **и** 1h | ~2 дня | `critical` — будить |
| Slow | 6 | 30m **и** 6h | ~5 дней | `warning` — тикет |

Два окна обязательны одновременно: короткое даёт быстрое срабатывание и быстрый
сброс, длинное не даёт кратковременному блипу поднять дежурного.

| SLI | SLO | Бюджет | Fast-порог | Slow-порог | Алерты |
|---|---|---|---|---|---|
| Ingest availability | 99.9% | 0.1% | 1.44% 5xx | 0.6% 5xx | `IngestErrorBudgetBurnFast` / `Slow` |
| Delivery success (по провайдеру) | 99% | 1% | 14.4% ошибок | 6% ошибок | `DeliveryErrorBudgetBurnFast` / `Slow` |

Доли считаются recording-правилами `nxs_anomaly:ingest_5xx:ratio_rate*` и
`nxs_anomaly:delivery_errors:ratio_rate*` — они же готовые панели для дашборда.

Чего эти правила **не** ловят: при нулевом трафике отношение равно `0/0 = NaN`,
любое сравнение с NaN ложно, и тихий инстанс молчит вместо алерта о делении на
ноль. Обратная сторона — полностью прекратившийся приём алертов burn rate не
видит; за это отвечают `WorkerCycleStuck` и `DatabaseUnavailable`.

`DeliverySuccessRatioLow` намеренно оставлен: он ловит проседание ниже 99% на
30-минутном окне, то есть срабатывает раньше slow-тира, но без привязки к
бюджету. Burn-rate-алерты отвечают на другой вопрос — «успеем ли мы до конца
месяца», а не «плохо ли сейчас».

## Grafana dashboard (быстрый старт)

Основные панели:

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

Для retention нет универсального alert rule: нулевой `increase` может означать
как остановившийся свип, так и отсутствие строк старше отсечки. Сопоставляйте
его с выбранными сроками и размером таблиц; readiness отдельно предупреждает о
невыбранной политике.
| Ingest latency p95 | `histogram_quantile(0.95, rate(nxs_anomaly_ingest_duration_seconds_bucket[5m]))` |
