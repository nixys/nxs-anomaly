# Настройка

*In English: [SETUP.md](../en/SETUP.md)*

Руководство по локальной настройке `nxs-anomaly`.

Если нужно понять, какие сущности создаются при `seed-demo`, как связаны алерты, группы, цепочки эскалации и уведомления, см. [DOMAIN_MODEL.md](DOMAIN_MODEL.md).

## Требования

- Go 1.25
- Node.js 22.12+ — только для локальной разработки frontend
- Docker или доступный экземпляр PostgreSQL 14+
- DSN PostgreSQL в переменной `NXS_ANOMALY_DB_DSN`

## Локальная разработка

Запустить PostgreSQL:

```bash
docker run --rm --name nxs-anomaly-postgres \
  -e POSTGRES_DB=nxs_anomaly \
  -e POSTGRES_USER=nxs_anomaly \
  -e POSTGRES_PASSWORD=nxs_anomaly \
  -p 127.0.0.1:5432:5432 \
  postgres:17-alpine
```

В другом терминале:

```bash
export NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:nxs_anomaly@127.0.0.1:5432/nxs_anomaly?sslmode=disable'

# Применить миграции и создать демо-данные
go run ./cmd/nxs-anomaly seed-demo --force

# Запустить API сервер
go run ./cmd/nxs-anomaly serve
```

Миграции применяются автоматически при инициализации store.

## Docker Compose

```bash
docker compose up --build
```

Сервисы:

| Сервис | Описание |
|---|---|
| `postgres` | PostgreSQL 17 |
| `app` | API сервер (`NXS_ANOMALY_START_SCHEDULER=false`) |
| `worker` | Background-worker (цикл каждые 5 сек) |
| `frontend` | nginx со SPA и same-origin proxy, <http://localhost:3100> |

Проверить работоспособность:

```bash
curl http://127.0.0.1:8080/health
curl http://127.0.0.1:8080/metrics
```

## CLI-справочник

```bash
# API сервер
go run ./cmd/nxs-anomaly serve
go run ./cmd/nxs-anomaly serve --no-scheduler   # без встроенного планировщика
go run ./cmd/nxs-anomaly serve --port 9090       # другой порт

# Worker
go run ./cmd/nxs-anomaly run-worker              # постоянный цикл
go run ./cmd/nxs-anomaly run-worker --once       # один цикл и выйти

# Данные
go run ./cmd/nxs-anomaly seed-demo --force       # сброс + демо-данные
go run ./cmd/nxs-anomaly print-state             # дамп всех коллекций из БД в JSON

# Просмотр
go run ./cmd/nxs-anomaly history                 # история групп
go run ./cmd/nxs-anomaly history --limit 50 --severity critical
go run ./cmd/nxs-anomaly alerts                  # список алертов
go run ./cmd/nxs-anomaly notifications           # список уведомлений

# Эскалации и проверки
go run ./cmd/nxs-anomaly run-escalations         # один проход эскалаций
go run ./cmd/nxs-anomaly healthcheck             # проверить /health
```

## Конфигурация базы данных

Приоритет при определении подключения:

1. `NXS_ANOMALY_DB_DSN` — полный DSN (рекомендуется):
   ```bash
   export NXS_ANOMALY_DB_DSN='postgres://user:password@host:5432/dbname?sslmode=disable'
   ```

2. Отдельные переменные (fallback):
   ```bash
   export NXS_ANOMALY_DB_HOST=127.0.0.1
   export NXS_ANOMALY_DB_PORT=5432
   export NXS_ANOMALY_DB_NAME=nxs_anomaly
   export NXS_ANOMALY_DB_USER=nxs_anomaly
   export NXS_ANOMALY_DB_PASSWORD=nxs_anomaly
   export NXS_ANOMALY_DB_SSLMODE=disable   # для production: require или строже
   ```

Параметры пула:

```bash
export NXS_ANOMALY_DB_POOL_MAX=10   # максимум соединений (по умолчанию 10)
export NXS_ANOMALY_DB_POOL_MIN=1    # минимум idle-соединений (по умолчанию 1)
export NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS=3600  # макс. возраст соединения (1ч); шеддинг после failover/через LB
export NXS_ANOMALY_DB_POOL_MAX_CONN_IDLE_SECONDS=1800      # макс. простой соединения (30м)
export NXS_ANOMALY_DB_POOL_HEALTHCHECK_SECONDS=60          # период health-probe пула (1м)
export NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=30         # statement_timeout на каждое соединение (0=выкл); миграции освобождены через SET LOCAL
export NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS=0           # retry подключения к БД на старте до N сек (0=fail-fast); рекомендуется >0 для serve/run-worker в k8s
```

## Прочие env vars

| Переменная | По умолчанию | Описание |
|---|---|---|
| `NXS_ANOMALY_ALERT_GROUP_TTL_DAYS` | `30` | Архивация resolved alert_groups старше N дней |
| `NXS_ANOMALY_CHATOPS_MESSAGES_TTL_DAYS` | `30` | Архивация chatops_messages старше N дней |
| `NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL` | — | URL для fire-and-forget уведомления о permanently failed notifications |
| `NXS_ANOMALY_PUBLIC_URL` | — | Публичный адрес этой инсталляции. Нужен для кнопок Mattermost: их callback идёт на абсолютный URL |
| `NXS_ANOMALY_MATTERMOST_ACTION_SECRET` | — | Секрет, которым аутентифицируется нажатие кнопки в Mattermost (свои callback'и он не подписывает) |
| `NXS_ANOMALY_SLACK_SIGNING_SECRET` | — | Signing secret Slack: им проверяются и слэш-команды, и нажатия кнопок |
| `NXS_ANOMALY_NOTIFY_ON_RESOLVE` | `false` | `true` — сообщать о закрытии алерта тем, кого он разбудил. Получатели берутся из самой группы, а не из расписания: это те, кто получил уведомление, а не те, кто дежурит сейчас. Отправляется один раз на группу |
| `NXS_ANOMALY_NOTIFICATION_MAX_RETRIES` | `3` | Максимум retry для уведомлений |
| `NXS_ANOMALY_NOTIFICATION_RETRY_DELAYS` | `1,5` | Задержки retry в минутах |
| `NXS_ANOMALY_WEBHOOK_TIMEOUT_SECONDS` | `5` | Timeout исходящих HTTP-webhook запросов |
| `NXS_ANOMALY_NOTIFICATION_DELIVERY_CONCURRENCY` | `8` | Параллельных provider-вызовов на цикл доставки/ретраев (`<1` — последовательно) |
| `NXS_ANOMALY_WORKER_CYCLE_TIMEOUT_SECONDS` | — (выкл) | Общий дедлайн одного worker-цикла; `0`/не задан — без ограничения (большой бэклог доставки легитимно может быть долгим) |
| `NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS` | `300` | TTL claim'а доставки: уведомление в `delivering`/`retrying` дольше N сек возвращается reaper'ом в очередь. **Должен превышать худшее время цикла**, иначе reaper перехватит активную доставку → дубль. При `<= WORKER_CYCLE_TIMEOUT` сервис логирует `claim_timeout_too_small` на старте |
| `NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT` | `false` | `true` — новый алерт на подтверждённой группе возвращает её в `open` и проводит цепочку с нулевого шага. По умолчанию повторный firing подтверждение не снимает — [ALERT_PROCESSING.md](ALERT_PROCESSING.md) §3.1 |
| `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS` | `false` | `true` — блокировать исходящие webhook/issue-запросы на приватные/loopback/link-local адреса (SSRF-защита) |
| `NXS_ANOMALY_DELIVERY_PROXY_URL` | — | Прокси для всей исходящей доставки: `http`/`https`/`socks5`/`socks5h`/`tcp` (прозрачный релей вида HAProxy `mode tcp`). Для сетей, где провайдер напрямую недоступен — [PROXY.md](PROXY.md) |
| `NXS_ANOMALY_DELIVERY_PROXY_<КАНАЛ>_URL` | — | Прокси для одного канала (`TELEGRAM`, `SLACK`, `MATTERMOST`, `WEBHOOK`, `CHATOPS`, `MOBILE`, `ISSUE`, `EMAIL`, `CALL`). `direct` — отправлять этот канал напрямую в обход общего прокси. `email` и `call` — только `socks5`/`tcp` |
| `NXS_ANOMALY_DELIVERY_NO_PROXY` | — | Хосты в обход прокси (синтаксис `NO_PROXY`: хост или домен, домен покрывает поддомены) |
| `NXS_ANOMALY_HTTP_READ_HEADER_TIMEOUT_SECONDS` | `5` | Таймаут чтения заголовков запроса (защита от slowloris) |
| `NXS_ANOMALY_HTTP_READ_TIMEOUT_SECONDS` | `15` | Таймаут чтения всего запроса |
| `NXS_ANOMALY_HTTP_WRITE_TIMEOUT_SECONDS` | `30` | Таймаут записи ответа |
| `NXS_ANOMALY_HTTP_IDLE_TIMEOUT_SECONDS` | `60` | Таймаут keep-alive idle-соединения |
| `NXS_ANOMALY_API_KEY` | — | Единый admin API-ключ для `/api/v1/*` (для автоматизации) |
| `NXS_ANOMALY_API_KEYS` | — | Несколько ключей с ролью: `tok1:admin,tok2:viewer`. Роли: admin/editor/responder/viewer |
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` | — | Break-glass админ: создаётся или повышается при каждом старте |
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD` | — | Его пароль, применяется заново при каждом старте |
| `NXS_ANOMALY_SESSION_TTL_SECONDS` | `43200` | Абсолютное время жизни веб-сессии (12 ч) |
| `NXS_ANOMALY_SESSION_COOKIE_SECURE` | `true` | `false` — только для локальной разработки по HTTP |
| `NXS_ANOMALY_ALLOW_ANONYMOUS` | `false` | `true` — открыть API без аутентификации (только локальная разработка) |
| `NXS_ANOMALY_AUDIT_RETENTION_DAYS` | `0` | Удалять события аудита старше N дней; `0` — хранить всё |
| `NXS_ANOMALY_NOTIFICATION_RETENTION_DAYS` | `0` | Удалять завершённые уведомления старше N дней |
| `NXS_ANOMALY_DELIVERY_ATTEMPT_RETENTION_DAYS` | `0` | Удалять записи о попытках доставки старше N дней |
| `NXS_ANOMALY_WEB_SESSION_RETENTION_DAYS` | `0` | Удалять web-сессии старше N дней (в них IP и User-Agent) |
| `NXS_ANOMALY_BLOCKED_CHANNELS` | — | Каналы, запрещённые на уровне инсталляции, через запятую |
| `NXS_ANOMALY_EGRESS_ALLOWLIST` | — | Разрешённые адресаты исходящей доставки: хосты и/или CIDR |
| `LOG_LEVEL` | `info` | Уровень логирования: `debug`/`info`/`warn`/`error`. `debug` включает диагностические логи (escalation_skipped, delivery_attempt и др.) |
| `LOG_FORMAT` | `text` | Формат логов: `text` или `json` |

Полная карта персональных данных, сроков хранения и процедур экспорта/удаления —
[DATA_INVENTORY.md](DATA_INVENTORY.md).

## Язык и часовой пояс интерфейса

SPA содержит каталоги `ru-RU` и `en-US`. До входа язык выбирается по настройкам
браузера и сохраняется в `localStorage`; после входа выбор записывается также в
профиль пользователя через `PUT /api/v1/auth/preferences`. Пустой `locale`
означает «следовать браузеру». `timezone` — IANA-имя, например
`Asia/Novosibirsk`; неизвестная зона отклоняется с HTTP 400.

Язык не задаётся переменной окружения всей инсталляции: это личная настройка.
Времена расписания всегда вычисляются в timezone расписания, а отображаются в
выбранной локали.

## Безопасность

- **Аутентификация обязательна**: без настроенных учётных данных `/api/v1/*` отклоняет все запросы. Открыть API можно только явно — `NXS_ANOMALY_ALLOW_ANONYMOUS=true`, и сервер предупреждает об этом при каждом старте.
- **Пользователи**: вход по логину/паролю, сессия в HttpOnly-куке `SameSite=Strict`. Пароли — PBKDF2-HMAC-SHA256, 600 000 итераций, соль на пользователя; токен сессии хранится хешем, поэтому дамп БД не содержит рабочих сессий. Смена или сброс пароля, снятие роли и удаление пользователя завершают все его сессии.
- **Первый вход**: `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` + `_PASSWORD`. Пара применяется при каждом старте, поэтому она же — путь восстановления при забытом пароле; после настройки обычных учёток её стоит убрать.
- **API-ключи** (автоматизация): `X-API-Key` или `Authorization: Bearer`, сравнение constant-time. Роль ключа ограничивает методы (403 при нехватке прав). Если запрос несёт и куку сессии, и ключ, побеждает сессия — в аудите останется человек, а не общий ключ.
- **Сессии**: `GET /api/v1/auth/sessions` показывает, где пользователь залогинен, `DELETE /api/v1/auth/sessions/{id}` завершает конкретную (только свою), `DELETE /api/v1/users/{id}/sessions` — админский «разлогинить везде» без сброса пароля.
- **API-ключи**: запись в `NXS_ANOMALY_API_KEYS` без `:role` теперь даёт **viewer**, а не admin (сервер предупреждает на старте). `NXS_ANOMALY_API_KEY` по-прежнему admin — это и есть явный scope, записанный в имени переменной.
- **Аудит**: `GET /api/v1/audit` (только admin), страница `/audit` в UI. Каждое событие несёт `request_id` — тот же, что в заголовке `X-Request-ID` и в access-логе, поэтому «всё, что сделал один запрос» — это один фильтр. Таблица append-only на уровне триггера; записываются имена изменённых полей, но не значения, чтобы секреты не появились во втором месте.
- **SSRF**: `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS=true` запрещает доставку на приватные диапазоны (включая cloud metadata `169.254.169.254`). Включайте в multi-tenant средах, где конфиг интеграций задаётся не доверенными операторами.
- **Probes**: используйте `/live` для k8s `livenessProbe` (не трогает БД) и `/health` или `/ready` для `readinessProbe` (DB ping). Полная справка по API — `docs/API.md`.

## Smoke-тест

Создать демо-данные и найти ключ интеграции:

```bash
go run ./cmd/nxs-anomaly seed-demo --force
go run ./cmd/nxs-anomaly print-state
```

Отправить webhook-алерт:

```bash
curl -X POST "http://127.0.0.1:8080/integrations/v1/webhook/<key>" \
  -H 'Content-Type: application/json' \
  -d '{
    "title": "CPU high",
    "labels": {
      "alertname": "CPUHigh",
      "service": "api",
      "severity": "critical"
    }
  }'
```

Отправить payload Alertmanager:

```bash
curl -X POST "http://127.0.0.1:8080/integrations/v1/alertmanager/<key>" \
  -H 'Content-Type: application/json' \
  -d '{
    "receiver": "platform",
    "status": "firing",
    "groupLabels": {"alertname": "HighLatency"},
    "commonLabels": {"service": "api", "severity": "critical"},
    "commonAnnotations": {"summary": "API latency is high"},
    "externalURL": "https://alertmanager.example.com",
    "alerts": [{
      "status": "firing",
      "labels": {"alertname": "HighLatency", "service": "api", "severity": "critical"},
      "annotations": {"summary": "API latency is high"},
      "startsAt": "2026-05-17T01:00:00Z",
      "endsAt": "0001-01-01T00:00:00Z",
      "generatorURL": "https://prometheus.example.com/graph",
      "fingerprint": "abc123"
    }]
  }'
```

Посмотреть группы алертов:

```bash
curl http://127.0.0.1:8080/api/v1/alert-groups
```

## Диагностика

Проверить подключение к БД:

```bash
go run ./cmd/nxs-anomaly healthcheck
```

Ответ `/health` содержит:

```json
{
  "status": "ok",
  "db_ok": true,
  "version": "dev",
  "uptime_seconds": 42,
  "worker_cycles_completed": 8,
  "last_worker_cycle_at": "2026-05-17T12:00:00Z"
}
```

При статусе `degraded`, `db_ok: false` и наличии поля `db_error` — проблема в подключении к PostgreSQL.

Если Docker-сборка падает из-за зависимостей — убедиться, что `vendor/` актуален:

```bash
go mod tidy
go mod vendor
docker build -t nxs-anomaly:local .
```
