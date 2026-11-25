# Тестирование

Проект проверяется на четырёх уровнях: быстрые unit- и статические проверки,
интеграция с PostgreSQL, браузерные сценарии и, наконец, развёртывание и
аварийное восстановление в kind. Список тестовых файлов меняется быстрее
документации; источники истины — `.github/workflows/ci.yml`,
`frontend/package.json` и каталоги `*_test.go`, `*.test.tsx`, `frontend/e2e`,
`deploy/helm/nxs-anomaly/tests`.

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

Обычный `go test ./...` не должен случайно подключаться к рабочей БД.
Тесты с PostgreSQL включаются только при установленной
`NXS_ANOMALY_TEST_DATABASE_URL`.

## Интеграционный набор PostgreSQL

Рекомендуемый локальный запуск:

```bash
tests/run_postgres_integration.sh
```

Скрипт использует существующий `NXS_ANOMALY_TEST_DATABASE_URL` либо запускает
одноразовый `postgres:17-alpine`, ждёт готовности и выполняет полный `go test`.
Настройки:

| Переменная | По умолчанию | Назначение |
|---|---:|---|
| `NXS_ANOMALY_TEST_DATABASE_URL` | — | DSN существующей PostgreSQL |
| `NXS_ANOMALY_TEST_POSTGRES_PORT` | `55432` | Порт локального контейнера |
| `NXS_ANOMALY_TEST_POSTGRES_DB` | `nxs_anomaly_test` | База данных |
| `NXS_ANOMALY_TEST_POSTGRES_USER` | `nxs_anomaly` | Пользователь |
| `NXS_ANOMALY_TEST_POSTGRES_PASSWORD` | `nxs_anomaly` | Пароль |
| `NXS_ANOMALY_KEEP_TEST_POSTGRES` | `0` | `1` оставляет контейнер работающим |
| `NXS_ANOMALY_TEST_GO_TEST_ARGS` | — | Дополнительные аргументы `go test`, используются job покрытия |
| `NXS_ANOMALY_TEST_TIMEOUT_SCALE` | `1` | Множитель предельного времени ожиданий набора |

**Набору нужна отдельная база данных.** Он очищает коллекции и выполняет
собственные worker cycles, ожидая доставку в локальные заглушки. Работающий
worker с той же БД захватит уведомления и доставит их в другое место — внешне
это выглядит как семь несвязанных ошибок тестов доставки. Набор читает heartbeat
worker (тот же, что использует readiness) и отказывается запускаться, если БД
занята.

Предельные времена набора рассчитаны на PostgreSQL рядом с процессом — так
устроен CI, но обычно не production. Один сетевой переход увеличивает цену
запроса примерно с 0,6 до 6,6 мс, и тесты начинают падать по времени, а не из-за
поведения. Для удалённой БД увеличьте `NXS_ANOMALY_TEST_TIMEOUT_SCALE`, например
до `4`.

Набор применяет миграции из `internal/store/migrations/` и проверяет настоящие
транзакции, ограничения и индексы, ingest, RBAC, аудит, сессии, расписания,
окна обслуживания, readiness, экспорт и удаление персональных данных, retention,
доставку и повторные попытки, несколько реплик worker и передачу trace context.

## Фронтенд

Требуется Node.js 24.15+ (LTS):

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

Vitest работает в jsdom. Набор покрывает страницы групп алертов, аудита,
интеграций, обслуживания, уведомлений, readiness, расписаний, настроек,
пользователей и первоначальной настройки, политики уведомлений и форматирование
i18n/Intl для `ru-RU` и `en-US`. Пороги покрытия находятся в
`frontend/vite.config.ts`.

`openapi:check` заново генерирует типы TypeScript из `docs/openapi.json` во
временный файл и сравнивает их с `frontend/src/api/schema.d.ts`. После
намеренного изменения контракта сначала выполните:

```bash
cd frontend
npm run openapi:types
```

Если rolldown сообщает о несовместимом native binding, `node_modules` установлен
для другой libc или платформы. Исправление — чистый `npm ci`, а не копирование
каталога из Alpine в glibc-окружение или наоборот.

## Браузерные end-to-end тесты

```bash
cd frontend
npm run e2e:install
npm run e2e
```

Playwright запускает настоящий Go API, PostgreSQL и Vite. Сценарии:

- работа дежурного: вход → ingest → acknowledge → resolve → доставка;
- первоначальная настройка через интерфейс;
- ротация расписания, overrides и покрытие;
- успешный тест провайдера и окончательно неудачная доставка;
- RBAC и границы Community Edition;
- первоначальная настройка, readiness и отчёт о резервной копии.

Карта сценариев и запуск в контейнере под WSL описаны в
`frontend/e2e/README.md`. Набор последовательный: сценарий readiness изменяет
общую БД и должен оставаться последним.

## Helm, развёртывание и аварийное восстановление

```bash
helm lint deploy/helm/nxs-anomaly --set inlineSecret.enabled=true --set postgresql.enabled=true
helm unittest -f 'tests/unit/*_test.yaml' deploy/helm/nxs-anomaly

deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh
deploy/helm/nxs-anomaly/tests/e2e/kind-networkpolicy.sh

tests/restore_drill.sh
tests/pitr_drill.sh
tests/run_load_test.sh
```

Unit-тесты Helm проверяют workloads, режимы секретов, production preflight,
масштабирование rate limit, NetworkPolicy, хранилища данных, Istio, Gateway API
и acceptance hook. Сценарии kind выполняют установку и обновление, проверяют
применение сетевых политик и реальные пути обновления. DR drills проверяют
логическое восстановление, point-in-time recovery, readiness и работу предыдущего
релиза с мигрированной схемой.

## Проверки контракта и безопасности

- `internal/server/openapi_contract_test.go` проверяет маршруты по
  `docs/openapi.json` в обе стороны, включая suffix/action routes и покрытие
  авторизацией.
- Публичный CI выполняет Go build/vet/race, lint, govulncheck, интеграцию с
  PostgreSQL, проверки фронтенда, документации и Helm. Точные команды и версии
  инструментов приведены в workflow.
- Регрессии production profile, secret references, редактирования секретных
  данных, same-origin и доставки покрыты соответствующими наборами;
  дополнительные локальные инструменты безопасности не становятся автоматически
  обязательными проверками публичного CI.
- `scripts/check-version.sh` сверяет `VERSION`, chart и метаданные OpenAPI.

## Pipeline

Публичные workflows: [ci.yml](https://github.com/nixys/nxs-anomaly/blob/main/.github/workflows/ci.yml)
и [release.yml](https://github.com/nixys/nxs-anomaly/blob/main/.github/workflows/release.yml).
Точные обязательные проверки и права на выпуск смотрите в них; локальные
скрипты также поддерживают более глубокие интеграционные, браузерные,
нагрузочные проверки и аварийное восстановление. Наличие локальной тестовой
команды не означает, что она выполняется в каждой job публичного CI.

Релизы по тегам публикуют образы API и фронтенда, Helm chart, SBOM и подписи,
после чего проверяют опубликованные артефакты.

## Минимум перед merge

Для изменения документации:

```bash
git diff --check
bash scripts/check-docs.sh
```

Для изменения Go или backend:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
golangci-lint run --timeout=15m ./...
tests/run_postgres_integration.sh
```

Для фронтенда или API-контракта добавьте `npm run test:coverage`,
`npm run build` и `npm run openapi:check`; для chart — `helm lint` и
helm-unittest. Изменения доставки, миграций, топологии или восстановления требуют
соответствующей интеграционной проверки, kind-сценария или drill — одних
unit-тестов недостаточно.

Соглашение о сообщениях коммитов приведено в
[CONTRIBUTING.md](../../../CONTRIBUTING.md).
