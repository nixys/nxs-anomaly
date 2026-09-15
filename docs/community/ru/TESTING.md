# Тестирование

*In English: [TESTING.md](../en/TESTING.md)*

Проект проверяется на четырёх уровнях: быстрые unit/static checks, интеграция с
PostgreSQL, браузерные сценарии, затем deployment/DR проверки в kind.
Список тестовых файлов меняется быстрее документации; источники истины —
`.github/workflows/ci.yml`, `frontend/package.json` и каталоги `*_test.go`, `*.test.tsx`,
`frontend/e2e`, `deploy/helm/nxs-anomaly/tests`.

## Быстрые проверки

Без внешних сервисов:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
golangci-lint run --timeout=15m ./...
git diff --check
```

Обычный `go test ./...` не должен случайно подключаться к runtime DB. Тесты,
которым нужен PostgreSQL, включаются только при
`NXS_ANOMALY_TEST_DATABASE_URL`.

## PostgreSQL integration suite

Рекомендуемый локальный запуск:

```bash
tests/run_postgres_integration.sh
```

Скрипт использует готовый `NXS_ANOMALY_TEST_DATABASE_URL` либо поднимает
одноразовый `postgres:17-alpine`, ждёт readiness и запускает полный `go test`.
Основные настройки:

| Переменная | По умолчанию | Назначение |
|---|---:|---|
| `NXS_ANOMALY_TEST_DATABASE_URL` | — | готовый PostgreSQL DSN |
| `NXS_ANOMALY_TEST_POSTGRES_PORT` | `55432` | порт локального контейнера |
| `NXS_ANOMALY_TEST_POSTGRES_DB` | `nxs_anomaly_test` | база |
| `NXS_ANOMALY_TEST_POSTGRES_USER` | `nxs_anomaly` | пользователь |
| `NXS_ANOMALY_TEST_POSTGRES_PASSWORD` | `nxs_anomaly` | пароль |
| `NXS_ANOMALY_KEEP_TEST_POSTGRES` | `0` | `1` — оставить контейнер |
| `NXS_ANOMALY_TEST_GO_TEST_ARGS` | — | дополнительные аргументы `go test`, используются coverage-job |
| `NXS_ANOMALY_TEST_TIMEOUT_SCALE` | `1` | множитель для wall-clock дедлайнов сюиты |

**Сюите нужна база в единоличном пользовании.** Она чистит коллекции и сама
крутит worker-циклы, ожидая, что доставки придут на её локальные стенды. Живой
worker, смотрящий в ту же базу, заберёт её уведомления себе и доставит их у
себя — со стороны это выглядит как падение семи не связанных между собой тестов
доставки. Сюита читает heartbeat воркера (тот же, что использует readiness) и
отказывается стартовать, если база занята.

Дедлайны сюиты рассчитаны на PostgreSQL рядом с процессом — так устроен CI и так
не устроен прод. Через один сетевой хоп запрос стоит ~6.6 мс вместо ~0.6 мс, и
тесты начинают падать по часам, а не по поведению. Для удалённой базы поднимите
`NXS_ANOMALY_TEST_TIMEOUT_SCALE` (например, `4`).

Suite применяет все миграции из `internal/store/migrations/` и проверяет реальные транзакции,
constraints/индексы, ingest, RBAC, audit, sessions, schedules,
maintenance, readiness, personal-data export/erase, retention, delivery/retry,
несколько worker-реплик и перенос trace context.

## Frontend

Нужен Node.js 22.12+:

```bash
cd frontend
npm ci
npm run typecheck
npm run test
npm run test:coverage
npm run build
npm run openapi:check
npm run audit:prod
npm run audit:dev
```

Vitest работает в jsdom. Набор покрывает страницы alert groups, audit,
integrations, maintenance, notifications, readiness, schedules, settings,
users, onboarding, notification policies и i18n/Intl formatting для `ru-RU` и
`en-US`. Coverage thresholds заданы в `frontend/vite.config.ts` и проверяются
`test:frontend`.

`openapi:check` заново генерирует TypeScript types из `docs/openapi.json` во
временный файл и сравнивает с `frontend/src/api/schema.d.ts`. После намеренного
изменения контракта сначала выполните:

```bash
cd frontend
npm run openapi:types
```

Если rolldown сообщает о несовместимом native binding, `node_modules` был
установлен под другую libc/платформу. Нужен чистый `npm ci`, а не копирование
каталога из Alpine в glibc-среду или наоборот.

## Browser e2e

```bash
cd frontend
npm run e2e:install
npm run e2e
```

Playwright поднимает реальный Go API, PostgreSQL и Vite. Сценарии:

- responder flow: вход → ingest → acknowledge → resolve → delivery;
- первичная настройка через UI;
- rotation, override и coverage расписания;
- успешный provider test и permanently failed delivery;
- RBAC и границы Community;
- setup/readiness и отчёт о резервной копии.

Карта и контейнерный запуск для WSL — `frontend/e2e/README.md`. Suite
последовательный: readiness-сценарий меняет общую БД и должен оставаться
последним.

## Helm, deployment и DR

```bash
helm lint deploy/helm/nxs-anomaly --set inlineSecret.enabled=true --set postgresql.enabled=true
helm unittest -f 'tests/unit/*_test.yaml' deploy/helm/nxs-anomaly

deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh
deploy/helm/nxs-anomaly/tests/e2e/kind-networkpolicy.sh

tests/restore_drill.sh
tests/pitr_drill.sh
tests/run_load_test.sh
```

Helm unit tests проверяют workloads, secret modes, production preflight,
rate-limit scaling, NetworkPolicy, базы, Istio/Gateway API и
acceptance hook. Kind-сценарии исполняют install/upgrade, сетевую политику и
реальные сценарии обновления. DR drill-ы проверяют логическое
восстановление, PITR, readiness и совместимость предыдущего релиза.

## Контрактные и security gates

- `internal/server/openapi_contract_test.go` двусторонне сверяет реальные routes
  и `docs/openapi.json`, включая suffix/action routes и authz coverage.
- Публичный CI запускает Go build/vet/race, lint, govulncheck, PostgreSQL
  integration, frontend, документационные и Helm-проверки. Точные команды и
  версии инструментов определяет workflow.
- Production profile, secret refs, redaction, same-origin и delivery regressions
  проверяются соответствующими тестами; дополнительные локальные инструменты
  безопасности не обязательно являются публичными CI gates.
- `scripts/check-version.sh` сверяет VERSION, chart и OpenAPI.

## CI

Публичные workflow: [ci.yml](https://github.com/nixys/nxs-anomaly/blob/main/.github/workflows/ci.yml) и
[release.yml](https://github.com/nixys/nxs-anomaly/blob/main/.github/workflows/release.yml). Они определяют обязательные
проверки Community; наличие локального скрипта не означает его запуск в каждом
публичном CI job. Локально доступны дополнительные browser, kind, load и DR проверки.
Теговый workflow публикует API/frontend, Helm chart, SBOM и подписи, затем
проверяет выпущенные артефакты.

## Минимум перед merge

Для документационного изменения:

```bash
git diff --check
bash scripts/check-docs.sh
```

Для Go/backend изменения:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
golangci-lint run --timeout=15m ./...
tests/run_postgres_integration.sh
```

Для frontend/API-контракта добавьте `npm run test:coverage`, `npm run build` и
`npm run openapi:check`; для chart — `helm lint` и helm-unittest. Изменения
delivery, migrations, topology и восстановления требуют соответствующего
integration/kind/drill сценария, а не только unit tests.

Конвенция commit-сообщений — [CONTRIBUTING.md](../../../CONTRIBUTING.md).
