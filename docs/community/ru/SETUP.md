# Настройка

Локальный запуск `nxs-anomaly` и настройки, с которыми вы встретитесь.
Готовые env-файлы, systemd, Compose и Kubernetes описаны в
[Установке](INSTALLATION.md).

## Требования

- Docker либо доступная PostgreSQL 14+
- Go 1.27 и Node.js 24.15+ (LTS) — только для сборки из исходников

## Docker Compose

Самый быстрый способ. Он скачивает опубликованные подписанные образы релиза —
без локальной сборки Go/npm и без registry, кроме GHCR — и запускает PostgreSQL,
API, worker и веб-интерфейс:

```bash
cp .env.example .env
# измените .env: NXS_ANOMALY_VERSION (тег из Releases, совпадающий с checkout),
# POSTGRES_PASSWORD и имя/пароль bootstrap admin. Compose откажется запускать
# контейнер, если обязательная переменная пуста, вместо подстановки
# предсказуемого значения по умолчанию.
docker compose up -d --wait --wait-timeout 180
```

`.env` хранится только локально — он уже включён в `.gitignore` — и всегда
обязателен: `docker compose up -d` без него сразу завершится с именем отсутствующей
переменной вместо запуска стека с пустым паролем.

| Сервис | Назначение |
|---|---|
| `postgres` | PostgreSQL 17, данные в именованном томе `pgdata` |
| `app` | API-сервер (`NXS_ANOMALY_START_SCHEDULER=false` — scheduler запускает worker) |
| `worker` | Фоновый цикл: эскалации, доставка, повторы, retention |
| `frontend` | nginx с интерфейсом и same-origin проксированием `/api` |

Данные переживают обычный `docker compose down` с последующим `up`: база
находится в именованном томе `pgdata`, а не внутри контейнера. Для намеренного
удаления установки `docker compose down -v` удаляет и этот том вместе с
контейнерами. Для нужных данных поддерживайте резервные копии.

Проверьте ответ:

```bash
curl http://127.0.0.1:8080/health
# {"status":"ok","db_ok":true,"edition":"community","version":"vX.Y.Z",...}

curl http://127.0.0.1:8081/ready
# собственная probe worker — {"status":"ok","db_ok":true,"worker_cycles_completed":N,...}.
# /health сервиса app здесь всегда сообщает worker_cycles_completed: 0: scheduler
# работает в worker (NXS_ANOMALY_START_SCHEDULER=false у app), поэтому этот ноль
# принадлежит другому процессу и не означает остановку worker.
```

Интерфейс доступен по <http://127.0.0.1:3100>. Войдите bootstrap admin из `.env`.
Вы увидите пустую, ненастроенную установку: демонстрационные данные описаны ниже
в [Проверке на демоданных](#seeing-it-work), а подключение собственных алертов —
на странице Setup в приложении.

Чтобы собрать образы из исходников, а не скачивать релиз — для разработки или
проверки ещё не выпущенного изменения — используйте `docker-compose.dev.yml` и
`.env.dev.example` вместо двух файлов выше:

```bash
cp .env.dev.example .env
docker compose -f docker-compose.dev.yml up -d --build
```

В этом варианте не нужно задавать `NXS_ANOMALY_VERSION`; остальное не меняется.

## Из исходников

Запустите БД:

```bash
docker run --rm --name nxs-anomaly-postgres \
  -e POSTGRES_DB=nxs_anomaly -e POSTGRES_USER=nxs_anomaly \
  -e POSTGRES_PASSWORD=nxs_anomaly \
  -p 127.0.0.1:5432:5432 postgres:17-alpine
```

В другом терминале:

```bash
export NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:nxs_anomaly@127.0.0.1:5432/nxs_anomaly?sslmode=disable'
export NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin
export NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD='pick a password'
export NXS_ANOMALY_SESSION_COOKIE_SECURE=false

go run ./cmd/nxs-anomaly serve --no-scheduler # API — миграции применятся автоматически
go run ./cmd/nxs-anomaly run-worker          # worker, в другом терминале
```

Миграции выполняются автоматически при инициализации store: отдельный шаг migrate
нельзя забыть. Вы получите пустую установку с аутентификацией; войдите во
фронтенд, запущенный ниже, как bootstrap admin. `seed-demo` (без `--force`, что
безопасно на пустой БД — см. [Проверку на демоданных](#seeing-it-work)) добавляет
демопользователей, расписание и интеграцию для ознакомления. Это способ увидеть
устройство продукта, а не обязательный шаг первого запуска.

Фронтенд собирается отдельно (Node.js 24.15+ (LTS)):

```bash
cd frontend
npm ci
npm run dev -- --port 3100   # http://127.0.0.1:3100, проксирует /api на :8080
```

## Проверка на демоданных {#seeing-it-work}

Чтобы заполнить пустую установку демонстрационными пользователями, расписанием,
цепочкой эскалации и интеграцией вместо немедленного подключения своих алертов:

```bash
go run ./cmd/nxs-anomaly seed-demo           # явно завершается ошибкой, если БД не пуста
go run ./cmd/nxs-anomaly print-state         # созданные данные в JSON
```

`--force` дополнительно сначала очищает каждую коллекцию, удаляя и демонстрационные,
и настоящие данные. Он предназначен для сброса временной установки, а не первого
запуска; используйте его только если хотите удалить всё существующее.

## Команды

| Команда | Назначение |
|---|---|
| `serve` | HTTP API и scheduler, если он не выключен |
| `run-worker` | Отдельный фоновый цикл для самостоятельного deployment |
| `run-escalations` | Один проход эскалаций и выход |
| `seed-demo` | Миграции и демонстрационная установка для ознакомления |
| `healthcheck --url` | Проверка endpoint; её запускает HEALTHCHECK образа |
| `print-state` | Все коллекции в JSON |
| `history` | История групп алертов (`--limit`, `--severity`, `--status`, `--integration`, `--from`, `--to`) |
| `alerts` | Нормализованные алерты (`--limit`, `--status`, `--severity`, `--integration`) |
| `notifications` | Уведомления (`--limit`, `--status`, `--user`, `--group`) |

`serve` принимает `--host`, `--port`, `--poll-interval`, `--no-scheduler`, `--api-key`,
`--tls-cert` и `--tls-key`; `run-worker` — `--poll-interval`, `--once` и
`--worker-addr`. Каждый флаг переопределяет переменную окружения с тем же смыслом.

## Сервер

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `NXS_ANOMALY_LISTEN_HOST` | `0.0.0.0` | Адрес прослушивания |
| `NXS_ANOMALY_LISTEN_PORT` | `8080` | Порт прослушивания |
| `NXS_ANOMALY_ADDR` | — | Полный `host:port`; переопределяет два значения выше |
| `NXS_ANOMALY_WORKER_ADDR` | `:8081` | Адрес `/live`, `/ready` и `/metrics` команды `run-worker` |
| `NXS_ANOMALY_TLS_CERT` / `_KEY` | — | Прямое обслуживание HTTPS; обычно TLS завершается на ingress |
| `NXS_ANOMALY_START_SCHEDULER` | `true` | `false` — `serve` не выполняет worker cycle; запускайте `run-worker` отдельно |
| `NXS_ANOMALY_POLL_INTERVAL` | `5` | Секунды между циклами worker при отсутствии пробуждений; ingest немедленно будит worker через PostgreSQL `LISTEN/NOTIFY`; запуски по пробуждению разделены минимум 200 мс, чтобы обрабатывать шторм пакетами |
| `NXS_ANOMALY_WORKER_CYCLE_TIMEOUT_SECONDS` | — (выкл) | Предельная длительность одного worker cycle; пусто или `0` — без ограничения: большой backlog доставки законно может обрабатываться долго |
| `NXS_ANOMALY_SHUTDOWN_TIMEOUT` | `10` | Секунды для завершения текущих запросов при SIGTERM |
| `NXS_ANOMALY_SHUTDOWN_DRAIN_SECONDS` | `0` (chart: `5`) | После SIGTERM продолжать обслуживание столько секунд с `/health` = 503, чтобы pod исчез из Service endpoints до закрытия listener |
| `NXS_ANOMALY_HTTP_READ_HEADER_TIMEOUT_SECONDS` | `5` | Время чтения заголовков запроса (защита от slowloris) |
| `NXS_ANOMALY_HTTP_READ_TIMEOUT_SECONDS` | `15` | Время чтения полного запроса |
| `NXS_ANOMALY_HTTP_WRITE_TIMEOUT_SECONDS` | `30` | Время записи ответа |
| `NXS_ANOMALY_HTTP_IDLE_TIMEOUT_SECONDS` | `60` | Время простоя keep-alive соединения |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. `debug` добавляет диагностические строки (`escalation_skipped`, `delivery_attempt` и другие) |
| `LOG_FORMAT` | `text` | `text` или `json` |

## База данных

Задайте полный DSN:

```bash
export NXS_ANOMALY_DB_DSN='postgres://user:pass@host:5432/nxs_anomaly?sslmode=require'
```

Либо его части, чтобы сервис собрал DSN сам:

```bash
export NXS_ANOMALY_DB_HOST=127.0.0.1
export NXS_ANOMALY_DB_PORT=5432
export NXS_ANOMALY_DB_NAME=nxs_anomaly
export NXS_ANOMALY_DB_USER=nxs_anomaly
export NXS_ANOMALY_DB_PASSWORD=…
export NXS_ANOMALY_DB_SSLMODE=disable      # production: require или строже
```

Пул соединений:

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `NXS_ANOMALY_DB_POOL_MAX` | `10` | Максимум соединений. Процесс с worker (`run-worker`, `serve` со scheduler) требует минимум 3: одно занимает LISTEN, ещё два — shard lock и транзакция под ним; с меньшим пулом запуск отклоняется |
| `NXS_ANOMALY_API_READ_CONCURRENCY` | Половина `NXS_ANOMALY_DB_POOL_MAX` | Одновременные API-чтения (GET) на процесс; остальной пул свободен для ingest, worker и записей. Ожидание чтения свыше 10 с даёт 503 + `Retry-After` |
| `NXS_ANOMALY_DB_POOL_MIN` | `1` | Сохраняемые простаивающие соединения; `0` не сохраняет ни одного |
| `NXS_ANOMALY_DB_POOL_MAX_CONN_LIFETIME_SECONDS` | `3600` | Обновление соединений после failover или при работе через балансировщик |
| `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS` | `30` | `statement_timeout` для каждого соединения; миграции исключены. `0` вообще не передаёт `statement_timeout` — требуется за PgBouncer, который отклоняет неразрешённые startup parameters |
| `NXS_ANOMALY_DB_POOL_MAX_CONN_IDLE_SECONDS` | `1800` | Закрытие соединения после такого простоя |
| `NXS_ANOMALY_DB_POOL_HEALTHCHECK_SECONDS` | `60` | Частота проверки простаивающих соединений |
| `NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS` | `0` | Повторять начальное подключение N секунд. В Kubernetes задавайте положительное значение: pod может стартовать раньше готовности БД |

**PostgreSQL за PgBouncer.** Поддерживается прямое подключение либо PgBouncer
с **session** pooling (`pool_mode = session`). **Transaction и statement
pooling не поддерживаются**: ни `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=0`,
ни увеличенный пул не заставят их работать, поскольку сервис использует состояние
сессии:

- миграции выполняются под session-level `pg_advisory_lock` с
  `SET statement_timeout = 0` / `RESET` на том же соединении;
- worker удерживает session-level `pg_try_advisory_lock` для каждого shard,
  пока работа под lock выполняется на других соединениях пула;
- worker держит одно соединение с постоянным `LISTEN` для пробуждения (может
  перейти к polling, но у блокировок выше запасного варианта нет);
- запросы используют кэшируемые prepared statements pgx.

В session pooling каждое соединение сервиса удерживает серверное соединение
весь срок жизни, поэтому рассчитывайте PgBouncer по сумме
`NXS_ANOMALY_DB_POOL_MAX` всех реплик API и worker (`default_pool_size` и
`max_client_conn`), а также задайте `NXS_ANOMALY_DB_STATEMENT_TIMEOUT_SECONDS=0`
либо добавьте `statement_timeout` в `ignore_startup_parameters`. Для prepared
statements в session mode дополнительная настройка не нужна. Поддержка transaction
pooling потребовала бы переноса координации на transaction-scoped locks или leases
и отдельного прямого подключения для listener и миграций; задача — GitHub #55.

`0` допустим только там, где таблица объясняет его смысл. Для размера пула,
времени жизни соединения, простоя и периода health check (`…_POOL_MAX`,
`…_MAX_CONN_LIFETIME_SECONDS`, `…_MAX_CONN_IDLE_SECONDS`, `…_HEALTHCHECK_SECONDS`),
HTTP timeouts и `NXS_ANOMALY_SESSION_TTL_SECONDS` минимум равен `1`: для драйвера
БД и HTTP-сервера `0` означает не «выключить», а «закрывать каждое соединение»
или «без timeout». Неразбираемое значение либо значение ниже минимума оставляет
умолчание; при старте пишется `invalid_setting` с именем переменной.

Результат нагрузочных проверок для расчёта ресурсов: увеличение пула помогает,
только если у БД есть свободный CPU. При ограниченной PostgreSQL больший пул
*ухудшает* пропускную способность, поскольку соединения увеличивают конкуренцию.

## Аутентификация

Выберите хотя бы один способ, прежде чем открывать сервис извне.

| Переменная | Назначение |
|---|---|
| `NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME` / `_PASSWORD` | Аварийный admin создаётся либо снова повышается при каждом запуске. Пароль каждый раз применяется заново; удалите обе переменные, когда появятся настоящие учётные записи |
| `NXS_ANOMALY_API_KEYS` | `tok1:admin,tok2:viewer` — ключи с ролями `admin`, `editor`, `responder`, `viewer`. Ключ без роли — viewer, а не admin |
| `NXS_ANOMALY_API_KEY` | Один ключ admin для автоматизации |
| `NXS_ANOMALY_SESSION_TTL_SECONDS` | `43200` — срок жизни браузерной сессии |
| `NXS_ANOMALY_TEAM_SCOPING` | `false`. `true` ограничивает каждого пользователя объектами его команд и объектами без команды; администраторы, API keys и worker не ограничены. См. [API.md](API.md#team-scoping) |
| `NXS_ANOMALY_SESSION_COOKIE_SECURE` | `true`. Ставьте `false` только для локального HTTP: Secure cookie по обычному HTTP принимается браузером, но не возвращается, поэтому вход выглядит успешным, а сессия не сохраняется |

Единый вход через OIDC-провайдера — возможность Enterprise. В этой сборке кнопка
входа отключена, а не скрыта; `GET /api/v1/capabilities` сообщает возможности
редакции.

## Доставка

| Переменная | Назначение |
|---|---|
| `NXS_ANOMALY_NOTIFICATION_MAX_RETRIES` | `3` |
| `NXS_ANOMALY_NOTIFICATION_RETRY_DELAYS` | `1,5` — минуты между попытками |
| `NXS_ANOMALY_WEBHOOK_TIMEOUT_SECONDS` | `5` |
| `NXS_ANOMALY_NOTIFICATION_DELIVERY_CONCURRENCY` | `8` параллельных вызовов провайдера за цикл |
| `NXS_ANOMALY_NOTIFICATION_CLAIM_TIMEOUT_SECONDS` | `300`. Доставка с более долгим claim возвращается в очередь. **Значение должно превышать худший worker cycle**, иначе reaper перехватит ещё выполняемую доставку и уведомление уйдёт дважды |
| `NXS_ANOMALY_DEAD_LETTER_WEBHOOK_URL` | Куда сообщать об окончательно неудачной доставке |
| `NXS_ANOMALY_WORKER_HEARTBEAT_URL` | Внешний dead-man's switch (healthchecks.io, Cronitor, push monitor Uptime Kuma). Worker отправляет `GET` после успешного цикла не чаще раза в минуту; прекращение ping означает отказ worker, БД либо сети. См. [ALERTING_RULES.md](ALERTING_RULES.md) |
| `NXS_ANOMALY_NOTIFY_ON_RESOLVE` | `false`. При включении закрытие алерта сообщается тем, кого разбудили: получатели берутся из группы, а не из текущего состава дежурных |

Учётные данные провайдеров email, Telegram, Slack и Mattermost описаны вместе с
каналами в [CONFIGURATION.md](CONFIGURATION.md): §7 — SMTP, §8 — чат-боты.
Исходящий прокси — в §9 того же документа и в [PROXY.md](PROXY.md).

### Телефонные звонки (Asterisk)

Канал `call` инициирует вызов через Asterisk Manager Interface (AMI, всегда
порт `5038`). Без экземпляра Asterisk уведомление `call` получает статус
`skipped`.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `NXS_ANOMALY_ASTERISK_HOST` | — | Имя хоста AMI, без порта |
| `NXS_ANOMALY_ASTERISK_USERNAME` / `_SECRET` | — | Учётные данные AMI |
| `NXS_ANOMALY_ASTERISK_CHANNEL` | — | Например `SIP/trunk/`; к значению добавляется номер телефона |
| `NXS_ANOMALY_ASTERISK_CONTEXT` / `_EXTEN` | — | Куда направить отвеченный звонок в dialplan |
| `NXS_ANOMALY_ASTERISK_PRIORITY` | `1` | Приоритет dialplan |
| `NXS_ANOMALY_ASTERISK_CALLER_ID` | `nxs-anomaly` | Caller ID, отображаемый на телефоне |
| `NXS_ANOMALY_ASTERISK_TRIGGER_VARIABLE` | `TRIGGER_MESSAGE` | Переменная канала с текстом алерта для dialplan |

`HOST`, `USERNAME`, `SECRET`, `CHANNEL`, `CONTEXT` и `EXTEN` обязательны;
отсутствие любого даёт `settings incomplete`. Для нескольких АТС нумеруйте те же
переменные с нуля — `NXS_ANOMALY_ASTERISK_0_HOST`,
`NXS_ANOMALY_ASTERISK_1_HOST` и далее без пропусков: первый отсутствующий номер
завершает список. Вызов пробует их по порядку до успеха. Если задан
`…_0_HOST`, ненумерованные переменные не читаются.

### Mobile push relay

| Переменная | Назначение |
|---|---|
| `NXS_ANOMALY_MOBILE_PUSH_URL` | Ваш relay, превращающий уведомление в настоящий push. Без него у канала `mobile` нет транспорта, уведомления получают `skipped`, а не `delivered`. Подключённому через приложение телефону он не нужен: телефон держит открытым event stream (см. [API.md](API.md)) |
| `NXS_ANOMALY_MOBILE_PUSH_TOKEN` | Необязательный `Authorization: Bearer` для relay |

## Защита

`NXS_ANOMALY_PROFILE=production` одновременно включает безопасные значения:
SSRF guard, rate limits запросов и запрет сохранения новых inline secrets.
Каждое из них можно задать отдельно. Circuit breaker доставки включён в любом
профиле.

Полезные параметры:

- `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS=true` — исходящая доставка отклоняет
  приватные, loopback и link-local адреса. Включён по умолчанию в production
  profile и values Helm chart; собственное умолчание бинаря — выключен.
  Получатель во внутренней сети разрешается через
  `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS_EXCEPT` (см.
  [SECURITY_PROFILE.md](SECURITY_PROFILE.md#exceptions-for-internal-receivers)).
- `NXS_ANOMALY_TRUSTED_PROXIES` — CIDR, чьим `X-Forwarded-For` доверяют.
  Клиентом считается ближайший адрес, не являющийся trusted proxy. Rate limit
  входа действует на адрес клиента: за недоверенным прокси все пользователи
  делят один лимит, и несколько неверных паролей блокируют всех. Helm chart по
  умолчанию доверяет приватным диапазонам — следовательно, клиент внутри них
  (VPN, офисная сеть, другой pod) выбирает адрес, по которому ограничивается.
  Поэтому неудачные входы также расходуют бюджет учётной записи: десять попыток
  с пополнением двенадцать в минуту; заголовок его не изменит. Если клиенты
  обращаются к API из приватной сети, сузьте список до реальных прокси.
- `NXS_ANOMALY_EGRESS_ALLOWLIST` — hostname и CIDR, к которым доставка вообще
  может обращаться.
- `NXS_ANOMALY_CIRCUIT_BREAKER_THRESHOLD` — число последовательных отказов
  одного канала и цели до срабатывания breaker (`5` в любом профиле; `0`
  выключает его), и `NXS_ANOMALY_CIRCUIT_BREAKER_COOLDOWN_SECONDS` (`30`) — время
  открытого состояния перед пробным вызовом.
- `NXS_ANOMALY_REOPEN_ACKED_ON_NEW_ALERT=true` — новый алерт в подтверждённой
  группе возвращает её в `open` и запускает цепочку с нулевого шага.
  По умолчанию выключено: повторный firing не отменяет подтверждение.
  См. [ALERT_PROCESSING.md](ALERT_PROCESSING.md), §3.1.

## Язык и часовой пояс интерфейса

Интерфейс содержит английский и русский языки и следует настройкам браузера;
если браузер не запросил ни один из них, используется английский. Пользователь
может переопределить язык и часовой пояс в своих настройках; выбор хранится на
пользователя, а не на всю установку.

## Smoke-тест

Создайте цепочку и интеграцию, затем отправьте в неё алерт:

```bash
KEY=…            # ключ интеграции из UI или POST /api/v1/integrations
curl -s -X POST "http://127.0.0.1:8080/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"alerts":[{"status":"firing",
        "labels":{"alertname":"DiskFull","severity":"critical","instance":"node-1"},
        "annotations":{"summary":"disk at 95%"}}]}'
# 202
```

Группа появляется в `GET /api/v1/alert-groups` в течение секунды; worker начинает
цепочку эскалации в следующем цикле. Повторная отправка того же алерта добавляет
его в ту же группу, не создавая новую; отправка с `"status":"resolved"` закрывает
группу и её алерты.

## Если что-то не работает {#when-something-does-not-work}

- **`/health` сообщает `db_ok: false`** — проблема в DSN или БД. При недоступной
  БД ingest отвечает `503` с `Retry-After`, чтобы отправители отложили повтор,
  а не потеряли алерт.
- **Алерты приходят, но уведомлений нет** — проверьте, что маршрут интеграции
  заканчивается цепочкой хотя бы с одним шагом, а выбранные люди имеют цель
  уведомления. `GET /api/v1/readiness` сообщает именно об этом и указывает
  неуспешную проверку.
- **Доставка предпринимается и завершается ошибкой** — ищите `delivery_failed`
  в логе worker; адрес, канал и ответ провайдера находятся в структурированных
  полях.
- **Ничего не происходит** — возможно, worker не работает. Проверяйте его
  *собственную* probe, а не API: запустите с `--worker-addr :8081` (Docker Compose
  уже так настроен) и запросите `/ready` на этом порту. Если
  `worker_cycles_completed` остаётся нулём либо `worker_stalled: true`,
  `run-worker` не запущен или его цикл завис. `/health` API тоже содержит
  `worker_cycles_completed`, но это счётчик данного *процесса*: с отдельным
  scheduler (`NXS_ANOMALY_START_SCHEDULER=false`, как в этом стеке) он всегда
  равен нулю и ничего не доказывает о worker.
