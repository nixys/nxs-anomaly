# Миграции

*In English: [MIGRATIONS.md](../en/MIGRATIONS.md)*

`nxs-anomaly` управляет схемой PostgreSQL через встроенные SQL-миграции.

## Расположение

Канонический путь:

```text
internal/store/migrations/*.sql
```

Файлы встроены в Go-бинарник при сборке:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS
```

Другие пути для миграций не используются. Старая папка Python-реализации `nxs_anomaly/migrations` удалена.

## Модель выполнения

Миграции запускаются автоматически при создании store:

```go
store.NewPostgreSQLStore(ctx)
```

Последовательность выполнения:

1. Подключиться к PostgreSQL;
2. Взять advisory lock `72544000`;
3. Создать `nxs_anomaly_schema_migrations` если не существует;
4. Отсортировать `migrations/*.sql` по имени файла;
5. Для каждого файла — проверить наличие версии в `nxs_anomaly_schema_migrations`;
6. Применить пропущенные миграции в отдельных транзакциях;
7. Записать применённую версию;
8. Освободить advisory lock.

Критические обновления данных используют отдельные advisory lock-ключи в движке, чтобы инициализация схемы и доменные операции не конкурировали за один lock.

## Таблица версий

```sql
CREATE TABLE IF NOT EXISTS nxs_anomaly_schema_migrations (
    version    TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Версия — имя файла без расширения `.sql`:

```text
0001_initial_schema
0002_typed_columns
```

## Текущие миграции

| Версия | Содержание |
|---|---|
| `0001_initial_schema` | Базовые JSONB-таблицы для всех сущностей и начальные expression-индексы. |
| `0002_typed_columns` | Типизированные колонки для hot-path полей: users, teams, schedules, integrations, chatops, mobile, alerts, alert\_groups, notifications. |
| `0003_constraints_and_notification_retries` | Retry-метаданные (`retry_count`, `next_retry_at`, `last_error`), уникальный индекс idempotency\_key, deferred FK-constraints. |
| `0004_alertmanager_channels_and_batches` | Колонка `source_type`, таблица `nxs_anomaly_notification_batches`, batch-колонки на notifications. |
| `0005_delivery_attempts` | Таблица `nxs_anomaly_notification_delivery_attempts` для аудита/дебага доставки. |
| `0006_duty_epic_templates` | Флаги дежурного на users, поля epic-отслеживания на alert\_groups. |
| `0007_notification_batches_group_idx` | Типизированные колонки `alert_group_id`/`integration_id` на пакетах, индекс для архивации завершённых групп. |
| `0008_delivery_scheduled_idx` | Частичный индекс для `status='delivery_scheduled'` (hot-path worker). |
| `0009_alerts_archival_idx` | Индекс `alert_group_id` на alerts для каскадной очистки при архивации. |
| `0010_validate_constraints` | `VALIDATE CONSTRAINT` для всех constraints, созданных с `NOT VALID` в более ранних миграциях. |
| `0011_grafana_compat` | Таблицы `nxs_anomaly_grafana_notification_policies`, `nxs_anomaly_grafana_channel_filters`, `nxs_anomaly_grafana_heartbeats` — хранение данных слоя совместимости с плагином Grafana OnCall. **Слой удалён**; таблицы остались и не используются, см. «Снятые таблицы» ниже. |
| `0012_kafka_outbox` | Таблица `nxs_anomaly_kafka_outbox` + индекс по `created_at` для transactional outbox (асинхронная отправка алертов в Kafka, FIFO-публикация worker-циклом). Схема одинакова в обеих редакциях — движение между ними должно быть заменой образа, — но заполняющий её продюсер входит только в enterprise-сборку; в community-сборке таблица существует и остаётся пустой. |
| `0013_soft_delete_integrations` | Колонка `deleted_at` на integrations + частичный индекс. Soft-delete: удалённые интеграции исключаются из list/ingest, но остаются в БД. |
| `0014_mobile_verification_tokens` | Таблица `nxs_anomaly_mobile_verification_tokens` + индекс по `expires_at`. Токены QR-верификации удалённого слоя совместимости; таблица не используется, см. «Снятые таблицы» ниже. |
| `0015_chatops_messages_created_at_idx` | Expression-индекс `((data->>'created_at'))` на chatops_messages — ускоряет TTL-архивацию старых сообщений. |
| `0016_due_groups_idx_predicate` | Пересоздание `nxs_anomaly_alert_groups_due_idx` с предикатом `status NOT IN ('resolved','silenced')` — выравнивание с фактическим запросом worker (старый предикат `status='open'` не покрывал acknowledged-группы). |
| `0017_notification_claim` | Частичный индекс `nxs_anomaly_notifications_claimed_idx` на `(status) WHERE status IN ('delivering','retrying')` для claim-then-deliver (multi-worker безопасная доставка) и reaper застрявших claim'ов. |
| `0018_audit_events` | Таблица `nxs_anomaly_audit_events` — неизменяемый (append-only) след всех мутирующих операций. Таймлайн группы (`data->'logs'`) для этого не годится: он ограничен `maxGroupLogs`, переписывается при каждой записи группы и покрывает только группы. Append-only держится триггером, а не грантами, потому что сервис владеет своей схемой и подключается владельцем таблицы; поэтому очистка требует осознанного отключения триггера. |
| `0019_identity_sessions` | Таблицы `nxs_anomaly_user_credentials` и `nxs_anomaly_web_sessions`. Учётные данные вынесены из `nxs_anomaly_users.data` намеренно: этот JSONB отдаётся дословно через обобщённые коллекционные эндпоинты, так что хеш пароля в нём был бы в одном `GET /api/v1/users` от раскрытия. Сессии хранят SHA-256 токена, а не сам токен — дамп базы не выдаёт живые сессии. |
| `0020_integration_team` | Колонка `team_id` на integrations. Граница команды проводится именно здесь: alerts и alert_groups уже несут `integration_id`, поэтому «группы моей команды» — это фильтр по нему, а не копия `team_id`, которая протухла бы при переназначении интеграции. `NULL` = не назначена и видна всем. |
| `0021_notification_integration` | Колонка `integration_id` на notifications, чтобы записи о доставке тоже скоупились по командам. Единственное место, где денормализация уместна: копия `team_id` протухла бы при переназначении интеграции, а копия `integration_id` — нет, потому что группа алертов не переезжает между интеграциями. До этого уведомления и попытки доставки (включая текст сообщения) читал любой аутентифицированный пользователь. |
| `0022_audit_request_id` | Колонка `request_id` на audit-событиях + частичный индекс по непустым значениям. Каждый запрос уже несёт `X-Request-ID` в access-логе и в ответах об ошибке; без него на строке аудита вопрос «что ещё произошло в том же запросе» решался сопоставлением таймстампов, которое перестаёт работать ровно тогда, когда нужно — под нагрузкой и когда один запрос порождает несколько событий. |
| `0023_notification_policy_runs` | Таблица `nxs_anomaly_notification_policy_runs` + индексы по `next_step_at` (активные) и по паре (группа, пользователь). Персональная политика уведомлений — это долговременный конечный автомат notify → wait → fallback, который ведёт worker-цикл; хранение в БД даёт переживание рестартов. Завершённые прогоны — не история, а служебные записи: помечаются `done` и вычищаются на этапе архивации. |
| `0024_login_rate_buckets` | Таблица `nxs_anomaly_rate_buckets` + индекс по `updated_at`. Token bucket лимитера входа в БД, чтобы лимит был общим на кластер, а не на под (иначе перебор паролей получал N попыток при N репликах). Строки эфемерные, чистятся retention-свипом воркера. См. [SECURITY_PROFILE.md](SECURITY_PROFILE.md). |
| `0025_audit_trace_id` | Колонка `trace_id` на audit-событиях + частичный индекс. Рядом с `request_id` из 0022: request id связывает то, что сделал один запрос, trace id — цепочку через процессы и время (ingest в API-поде и доставка в worker-цикле через час — разные запросы). См. [TRACING.md](TRACING.md). |
| `0026_maintenance_windows` | Таблица `nxs_anomaly_maintenance_windows` + индекс по `ends_at`. Плановые работы: интеграции, перечисленные в окне, в его границах записывают алерты, но не пейджат — группа создаётся сразу `silenced`, а dead-man switch по ним молчит. Expand-only, ничего вне фичи таблицу не читает, поэтому откат на предыдущий релиз просто перестаёт её смотреть. См. `internal/engine/maintenance.go`. |
| `0027_retention_indexes` | Индексы под retention-свип: частичный по `updated_at` терминальных notifications, по `created_at` попыток доставки и web-сессий, плюс индекс по `actor_id` в аудите. Свип задаёт один и тот же вопрос («самые старые N строк старше отсечки») каждый цикл воркера — без индексов это seq scan самых больших таблиц раз в несколько секунд, ровно на той инсталляции, где они велики потому, что retention только что включили. Индекс по актору обслуживает и псевдонимизацию при удалении пользователя. См. `internal/store/store_retention.go` и [DATA_INVENTORY.md](DATA_INVENTORY.md). |
| `0028_oncall_quality_reports` | Хранение отчётов качества дежурств: период, команда, дата генерации, версия формата и JSON-дайджест; индекс `(team_id, generated_at)`. |

## Связь с кодом store

`internal/store/store.go` определяет:

- `EntityTables` — отображение имени коллекции → таблица PostgreSQL;
- `TypedColumns` — дополнительные типизированные колонки, записываемые при upsert;
- `typedValues` — значения для этих колонок.

При добавлении или удалении типизированной колонки в миграции нужно синхронно обновить `TypedColumns` и `typedValues`. Integration-тесты покрывают этот путь, так как store пишет через реальные миграции.

Описание доменных сущностей, их связей и жизненных циклов находится в [DOMAIN_MODEL.md](DOMAIN_MODEL.md). Миграции должны сохранять совместимость с этой моделью: таблицы хранят полный JSONB-документ, а типизированные колонки являются индексируемым представлением hot-path полей.

## Добавление новой миграции

1. Создать следующий по порядку файл:

```text
internal/store/migrations/0012_short_description.sql
```

2. Сделать операции идемпотентными где возможно:

```sql
ALTER TABLE nxs_anomaly_alert_groups
    ADD COLUMN IF NOT EXISTS example text;

CREATE INDEX IF NOT EXISTS nxs_anomaly_example_idx
    ON nxs_anomaly_alert_groups (example);
```

3. Если миграция добавляет типизированную колонку — обновить `TypedColumns` и `typedValues` в `store.go`.

4. Добавить или обновить тесты, покрывающие новый путь в схеме.

5. Проверить:

```bash
go test ./...
tests/run_postgres_integration.sh
```

## Операционные рекомендации

- Миграции автоматические — отдельного CLI для их запуска нет.
- Делать резервную копию БД перед production-обновлениями.
- При тяжёлых миграциях (backfill большой таблицы) сначала развернуть один экземпляр.
- Избегать блокирующих DDL в обычной миграции. Предпочтительный паттерн: additive nullable column → backfill → index → validate.
- Использовать `NOT VALID` при добавлении constraints на существующие данные, затем валидировать в отдельной миграции (как сделано в `0010_validate_constraints`).

### Неблокирующие миграции (`CREATE INDEX CONCURRENTLY`)

Обычная миграция выполняется в транзакции, что несовместимо с `CREATE INDEX CONCURRENTLY` (и берёт ACCESS EXCLUSIVE-лок на больших таблицах). Чтобы добавить индекс без блокировки, начните файл миграции с маркера:

```sql
-- nxs:no-transaction
CREATE INDEX CONCURRENTLY IF NOT EXISTS nxs_anomaly_example_idx
    ON nxs_anomaly_alerts (example);
```

Такая миграция выполняется **вне транзакции** (`runMigrationNoTx`), поэтому **обязана быть идемпотентной** (`IF NOT EXISTS`): при сбое она может остаться частично применённой и незаписанной в `nxs_anomaly_schema_migrations`. После неудачного `CONCURRENTLY` PostgreSQL оставляет INVALID-индекс — его нужно удалить (`DROP INDEX`) перед повтором.

## Проверка применённых версий

```sql
SELECT version, applied_at
FROM nxs_anomaly_schema_migrations
ORDER BY version;
```

Ожидаемые версии проверяются Go integration-тестами.

## Снятые таблицы

Слой совместимости с плагином Grafana OnCall удалён вместе с сущностью
`grafana_plugins` и токенами QR-верификации. Четыре таблицы остались в схеме и
больше ничем не читаются:

- `nxs_anomaly_grafana_plugins` (из `0001`);
- `nxs_anomaly_grafana_notification_policies`, `nxs_anomaly_grafana_channel_filters`,
  `nxs_anomaly_grafana_heartbeats` (из `0011`);
- `nxs_anomaly_mobile_verification_tokens` (из `0014`).

Они не удалены в том же релизе намеренно. Правило expand/contract из
[BACKUP_RESTORE.md](BACKUP_RESTORE.md) требует, чтобы предыдущий бинарь работал
на мигрированной схеме — это проверяет drill отката. `DROP TABLE` в релизе,
который снимает код, сделал бы откат на предыдущий тег невозможным: тот ещё
обращается к этим таблицам.

Снять их следует отдельной миграцией в **следующем** релизе, когда откат на
версию со слоем перестанет быть сценарием.
