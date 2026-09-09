# Развёртывание

*In English: [DEPLOY.md](../en/DEPLOY.md)*

`nxs-anomaly` — Go-бинарник, работающий с PostgreSQL. Может запускаться как единый процесс со встроенным планировщиком или как раздельные API и worker-процессы.

Production-конфигурация рекомендует раздельные компоненты:

- **API**: `nxs-anomaly serve --no-scheduler` (несколько реплик)
- **Worker**: `nxs-anomaly run-worker --poll-interval 5` (несколько реплик безопасны: доставка/ретраи — атомарный claim, эскалация шардируется по интеграции)

Оба компонента используют одну БД и одни встроенные миграции.

Доменная модель и жизненные циклы сущностей описаны в [DOMAIN_MODEL.md](DOMAIN_MODEL.md). Это полезно при настройке retention, алертов на метрики и дашбордов по таблицам PostgreSQL.

## Контейнерный образ

### Именование артефактов релиза

Одно правило на все артефакты: **`<продукт>[-компонент]-<издание>`, издание всегда
последним сегментом**. Отсюда:

| Артефакт | Community | Enterprise |
|---|---|---|
| Бэкенд | `nxs-anomaly` | `nxs-anomaly` |
| Фронтенд | `nxs-anomaly-frontend` | `nxs-anomaly-frontend` |
| Helm chart | `nxs-anomaly` | `nxs-anomaly` |

Community публикуется в GHCR, enterprise — в приватный реестр по учётной записи,
выдаваемой на инсталляцию. Тег у всех один и тот же: `vX.Y.Z`, он же `appVersion`
чарта, поэтому `image.tag` в values можно не задавать.

Образ фронтенда собирается отдельно под каждое издание, хотя его исходники
одинаковы: интерфейс не содержит кода, зависящего от издания, — он спрашивает
сервер через `/api/v1/auth/methods` и `/api/v1/capabilities`. Дублирование принято
сознательно, чтобы не заставлять читателя манифеста помнить, что один артефакт из
трёх живёт по другому правилу.

Имя чарта **не** попадает в имена объектов: `values.yaml` фиксирует `nameOverride`,
поэтому оба издания рендерят одинаковые имена и одинаковые селекторы Deployment.
Это и делает переход между изданиями обычным `helm upgrade` — селектор менять
нельзя, и без этой фиксации переход был бы переустановкой с потерей PVC.

### Сборка

```bash
docker build \
  --build-arg VERSION=1.2.3 \
  -t ghcr.io/nixys/<image-project>/nxs-anomaly:v1.2.3 .
docker push ghcr.io/nixys/<image-project>/nxs-anomaly:v1.2.3
```

Для инъекции версии в бинарник добавить в `Dockerfile` аргумент:

```dockerfile
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
    -ldflags="-s -w -X github.com/nixys/nxs-anomaly/internal/server.Version=${VERSION}" \
    -o /out/nxs-anomaly ./cmd/nxs-anomaly
```

После этого `/health` будет возвращать переданную версию в поле `version`.

Характеристики образа:

- многоэтапная сборка, `CGO_ENABLED=0`;
- runtime-слой: `gcr.io/distroless/static-debian12:nonroot`;
- зависимости берутся из `vendor/` (`-mod=vendor`);
- Docker HEALTHCHECK через `nxs-anomaly healthcheck`;
- порт `8080`.

## PostgreSQL

Создать базу и пользователя:

```sql
CREATE USER nxs_anomaly WITH PASSWORD 'change-me';
CREATE DATABASE nxs_anomaly OWNER nxs_anomaly;
```

Задать DSN:

```bash
NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:change-me@postgres:5432/nxs_anomaly?sslmode=disable'
```

Миграции применяются автоматически при старте. Подробности — [MIGRATIONS.md](MIGRATIONS.md).

## Kubernetes

### Helm chart (рекомендуемый способ)

Официальный чарт — [`deploy/helm/nxs-anomaly`](../../../deploy/helm/nxs-anomaly)
(см. его [README](../../../deploy/helm/nxs-anomaly/README.md)). Разворачивает API, worker
и frontend; хранилища — встроенные (`*.enabled`, single-node для
dev/test) или внешние (`external*`).
Секреты — из существующего Secret (`existingSecret`), External Secrets
(`externalSecrets`) или Vault Secrets Operator (`vaultSecretOperator`); inline —
только для dev. Есть PDB, NetworkPolicy, topologySpread, ServiceMonitor и
PrometheusRule. Внешний трафик можно опубликовать через обычный Ingress, Istio
`VirtualService`/`Gateway` или Kubernetes Gateway API `HTTPRoute`/`Gateway`;
все три механизма выключены по умолчанию и независимы.

```bash
# Прод: внешний PostgreSQL + оператор-управляемый Secret
kubectl create secret generic nxs-anomaly-env \
  --from-literal=NXS_ANOMALY_DB_DSN='postgres://user:pass@pg.prod:5432/nxs_anomaly?sslmode=require'
helm install nxs-anomaly deploy/helm/nxs-anomaly \
  --set existingSecret.enabled=true --set existingSecret.name=nxs-anomaly-env \
  --set ingress.enabled=true --set ingress.host=nxs-anomaly.example.com
```

Проверки чарта: `helm lint`, `helm unittest -f 'tests/unit/*_test.yaml'` и реальный
install/upgrade smoke в kind — `deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh`
(в CI — job'ы `helm:test` и `helm:kind`).

Ручные YAML-манифесты ниже описывают то же «под капотом» — для окружений без Helm
или для понимания того, что рендерит чарт.

### Секреты

```bash
kubectl create namespace nxs-anomaly

kubectl -n nxs-anomaly create secret generic nxs-anomaly-config \
  --from-literal=NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:change-me@postgres.nxs-anomaly.svc.cluster.local:5432/nxs_anomaly?sslmode=disable' \
  --from-literal=NXS_ANOMALY_API_KEY='<случайный-ключ>'
```

Секреты провайдеров (опционально, можно в тот же Secret или отдельный):

```bash
kubectl -n nxs-anomaly create secret generic nxs-anomaly-providers \
  --from-literal=NXS_ANOMALY_TELEGRAM_BOT_TOKEN='<токен>' \
  --from-literal=NXS_ANOMALY_SMTP_HOST='smtp.example.com' \
  --from-literal=NXS_ANOMALY_SMTP_USERNAME='alerts@example.com' \
  --from-literal=NXS_ANOMALY_SMTP_PASSWORD='<пароль>'
```

### Deployment: API

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nxs-anomaly-api
  namespace: nxs-anomaly
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nxs-anomaly-api
  template:
    metadata:
      labels:
        app: nxs-anomaly-api
    spec:
      containers:
        - name: api
          image: ghcr.io/nixys/<image-project>/nxs-anomaly:v1.2.3
          args: ["serve", "--host", "0.0.0.0", "--port", "8080", "--no-scheduler"]
          ports:
            - name: http
              containerPort: 8080
          envFrom:
            - secretRef:
                name: nxs-anomaly-config
            - secretRef:
                name: nxs-anomaly-providers
                optional: true
          readinessProbe:
            httpGet:
              path: /health   # includes a DB ping; pod removed from Service if DB is down
              port: http
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /live      # cheap, never touches the DB — a DB blip won't restart the pod
              port: http
            periodSeconds: 20
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
```

### Deployment: Worker

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nxs-anomaly-worker
  namespace: nxs-anomaly
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nxs-anomaly-worker
  template:
    metadata:
      labels:
        app: nxs-anomaly-worker
    spec:
      containers:
        - name: worker
          image: ghcr.io/nixys/<image-project>/nxs-anomaly:v1.2.3
          args: ["run-worker", "--poll-interval", "5"]
          ports:
            - name: telemetry
              containerPort: 8081   # /live, /ready, /metrics — NXS_ANOMALY_WORKER_ADDR
          envFrom:
            - secretRef:
                name: nxs-anomaly-config
            - secretRef:
                name: nxs-anomaly-providers
                optional: true
          readinessProbe:
            httpGet:
              path: /ready         # DB reachable AND worker cycles are completing
              port: telemetry
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /live          # cheap, never touches the DB
              port: telemetry
            initialDelaySeconds: 5
            periodSeconds: 10
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
```

Отдельный `run-worker` поднимает собственный телеметрийный HTTP-эндпоинт (по
умолчанию `:8081`, см. `NXS_ANOMALY_WORKER_ADDR`): `/live`, `/ready`, `/metrics`.
Это закрывает слепое пятно наблюдаемости — раньше при схеме `serve --no-scheduler`
+ `run-worker` delivery-метрики (латентность доставки, dead-letter, breaker,
skipped/shift, coverage) не экспонировались вовсе, потому что реестр Prometheus
жил только в HTTP-сервере API. `/ready` отражает не только доступность БД, но и то,
что worker реально **завершает циклы**: зависший цикл (застрявший запрос, дедлок)
выводит реплику из readiness. Порог простоя — `NXS_ANOMALY_WORKER_STALL_TIMEOUT_SECONDS`
(по умолчанию `max(30s, 4×poll)`).

Несколько реплик worker'а безопасны. Доставка и ретраи выбираются атомарным claim
(`SELECT … FOR UPDATE SKIP LOCKED`, уникальный `worker_id` на процесс), эскалация
шардируется по интеграции через per-shard advisory-локи, а брошенные claim-ы
реклеймятся (`ReclaimStaleClaims`). Отсутствие двойной доставки под конкуренцией
подтверждено `TestMultiWorkerNoDoubleDelivery` и профилями `worker_kill`/`retry_storm`
нагрузочного harness (см. [CAPACITY.md](./CAPACITY.md)). Конкуренция — не то же, что
крах: реплика, убитая между отправкой провайдеру и сохранением, отдаёт claim реапперу,
и уведомление уходит второй раз. Это at-least-once, гарантия здесь именно такая, и
harness сверяет дубли с числом реклеймов, а не с нулём. Единственная реплика лишь
упрощает порядок событий, но не является требованием корректности.

При `SIGTERM` worker не стартует новые циклы, даёт текущему `RunWorkerCycle`
завершиться и штатно гасит телеметрийный HTTP-сервер. API-сервер при shutdown
останавливает приём новых HTTP-запросов через `http.Server.Shutdown` и также ждёт
встроенный scheduler, если он включён.

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nxs-anomaly-api
  namespace: nxs-anomaly
spec:
  selector:
    app: nxs-anomaly-api
  ports:
    - name: http
      port: 8080
      targetPort: http
```

Открыть через ingress-контроллер. Источники webhook-алертов нуждаются в доступе к `/integrations/v1/*`.

## Метрики

Prometheus scrape endpoint:

```text
GET /metrics
```

Метрики реализованы через `prometheus/client_golang` SDK (кастомный registry на инстанс сервера):

| Метрика | Тип | Описание |
|---|---|---|
| `nxs_anomaly_alerts_ingested_total` | counter | Всего принятых алертов |
| `nxs_anomaly_alert_groups_open` | gauge | Открытые группы (из БД при каждом scrape) |
| `nxs_anomaly_notifications_delivered_total` | counter | Всего доставленных уведомлений |
| `nxs_anomaly_notifications_failed_total` | counter | Всего провалившихся уведомлений |
| `nxs_anomaly_schedules_degraded` | gauge | Расписания с пробелами/выключенные/с несуществующими участниками, через которые ходит escalation chain без `allow_uncovered` |
| `nxs_anomaly_schedules_total` | gauge | Всего расписаний |
| `nxs_anomaly_sources_silent` | gauge | Интеграции с объявленным heartbeat, не приславшие ничего в срок. Молчание источника снаружи неотличимо от здоровья |
| `nxs_anomaly_duty_on_call` | gauge | Сколько человек расписания ставят на дежурство прямо сейчас |
| `nxs_anomaly_duty_without_checkin` | gauge | Из них не подтвердили смену. Покрытие отвечает «назначен ли кто-то», эта метрика — «ответил ли он» |
| `nxs_anomaly_shift_notifications_total` | counter | Уведомления о начале смены дежурства |
| `nxs_anomaly_notifications_skipped_total` | counter | Уведомления без транспорта (`channel`, `reason`); никогда не считаются доставленными |
| `nxs_anomaly_retention_deleted_total` | counter | Строки, удалённые retention-свипом (`category`). Категория с заданным сроком и плоским счётчиком — свип, который перестал работать. См. [DATA_INVENTORY.md](DATA_INVENTORY.md) |
| `nxs_anomaly_notifications_retry_queue_depth` | gauge | Уведомлений в retry-очереди (из БД) |
| `nxs_anomaly_notification_batches_pending` | gauge | Открытых пакетов (из БД) |
| `nxs_anomaly_groups_archived_total` | counter | Архивированных resolved-групп |
| `nxs_anomaly_worker_cycles_total` | counter | Завершённых worker-циклов |
| `nxs_anomaly_worker_cycle_duration_seconds` | gauge | Длительность последнего цикла |
| `nxs_anomaly_worker_last_cycle_timestamp_seconds` | gauge | Unix-время последнего цикла (для детекта простоя: `time() - метрика`; НЕ путать с duration) |
| `nxs_anomaly_db_up` | gauge | 1 если последний ping БД прошёл, иначе 0 |
| `nxs_anomaly_notifications_delivered_by_provider_total` | counter | Успешные доставки по каналу (`provider`); в паре с `_delivery_errors_by_provider_total` даёт per-provider success-ratio SLI |
| `nxs_anomaly_oldest_due_escalation_age_seconds` | gauge | Возраст самой старой просроченной группы |
| `nxs_anomaly_oldest_pending_delivery_age_seconds` | gauge | Возраст самого старого `delivery_scheduled` |
| `nxs_anomaly_stale_claims_reclaimed_total` | counter | Реклейм брошенных claim-ов (краш worker'а/короткий claim-timeout) |

И worker, и API экспонируют один и тот же набор — worker на своём порту
(`:8081` по умолчанию). Delivery-метрики производит worker, поэтому при
разнесённой схеме скрейпить нужно **оба** таргета.

```yaml
scrape_configs:
  - job_name: nxs-anomaly-api
    static_configs:
      - targets: ['nxs-anomaly-api.nxs-anomaly.svc.cluster.local:8080']
  - job_name: nxs-anomaly-worker
    static_configs:
      - targets: ['nxs-anomaly-worker.nxs-anomaly.svc.cluster.local:8081']
```

Полный набор алертов (`PrometheusRule`), дашборд Grafana, определения SLI/SLO и
нагрузочно-хаос envelope:

- [ALERTING_RULES.md](./ALERTING_RULES.md) — правила и SLI/SLO с PromQL;
- [prometheus-rules.yaml](../../prometheus-rules.yaml) — готовый `PrometheusRule` CRD;
- [grafana-dashboard.json](../../grafana-dashboard.json) — импортируемый дашборд;
- [CAPACITY.md](./CAPACITY.md) — профили нагрузки, сценарии сбоев, capacity envelope.

## Аутентификация API

Для людей используйте парольные сессии в HttpOnly cookie, для автоматизации
— явно scoped API keys. Legacy `NXS_ANOMALY_API_KEY` всегда admin; записи
`NXS_ANOMALY_API_KEYS` должны иметь роль (`token:viewer`, `token:editor`, …).
Ключ передаётся одним из заголовков:

```text
X-API-Key: <ключ>
Authorization: Bearer <ключ>
```

Webhook-эндпоинты аутентифицируются по ключу интеграции в URL. Дополнительная HMAC-валидация payload включается на уровне конкретной интеграции через поле `webhook_secret`.

Перед production rollout явно выберите retention и egress policy:

- четыре `*_RETENTION_DAYS` — сроки аудита, уведомлений, attempts и сессий;
- `NXS_ANOMALY_BLOCKED_CHANNELS` и `NXS_ANOMALY_EGRESS_ALLOWLIST` — разрешённые
  каналы и назначения.

`GET /api/v1/readiness` показывает незакрытые решения. Полный перечень данных —
[DATA_INVENTORY.md](DATA_INVENTORY.md), безопасные дефолты —
[SECURITY_PROFILE.md](SECURITY_PROFILE.md).

## Порядок обновления

1. Собрать и опубликовать новый образ.
2. Проверить новые миграции в `internal/store/migrations/`.
3. Сделать резервную копию БД.
4. Развернуть один pod API или worker — миграции применятся ровно один раз (advisory lock `72544000`).
5. Развернуть остальные поды API.
6. Smoke-тест:

```bash
curl https://nxs-anomaly.example.com/health
curl https://nxs-anomaly.example.com/metrics
```

Параллельный старт нескольких экземпляров безопасен — миграционный runner защищён advisory lock.
