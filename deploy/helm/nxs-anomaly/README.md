# Helm chart nxs-anomaly

Официальный chart для [nxs-anomaly](../../../README.md): API, worker и веб-интерфейс,
с подключаемыми сервисами данных и выбираемым источником секретов.

## Быстрый старт

Разработка / kind (всё встроенное, dev-секрет рендерится из values):

```bash
helm install nxs-anomaly deploy/helm/nxs-anomaly \
  --set inlineSecret.enabled=true \
  --set postgresql.enabled=true
```

Production (OCI-chart + внешний managed PostgreSQL + production-пресет):

```bash
VERSION=0.1.95          # устанавливаемый релиз; образы имеют тег v$VERSION
HELM_PROJECT="<значение HARBOR_HELM_PROJECT>"

# 1. Создать Secret как минимум с NXS_ANOMALY_DB_DSN (и учётными данными провайдеров).
kubectl create secret generic nxs-anomaly-env \
  --from-literal=NXS_ANOMALY_DB_DSN='postgres://user:pass@pg.prod:5432/nxs_anomaly?sslmode=require'
# 2. Аутентифицироваться и получить chart (production-пресет лежит внутри него).
echo "$HELM_REGISTRY_PASSWORD" | helm registry login ghcr.io/nixys \
  -u "$HELM_REGISTRY_USER" --password-stdin
helm pull "oci://ghcr.io/nixys/nxs-anomaly" \
  --version "$VERSION" --untar
# 3. Установить с пресетом.
helm install nxs-anomaly nxs-anomaly/ \
  -f nxs-anomaly/values-production.yaml \
  --set existingSecret.name=nxs-anomaly-env \
  --set externalPostgres.host=pg.prod \
  --set ingress.enabled=true --set ingress.host=nxs-anomaly.example.com
```

### Правило «одна версия, один префикс»

| Строка | Форма | Где |
|---|---|---|
| Git-тег | `v$VERSION` | на что реагирует релизный пайплайн |
| Теги образов | `v$VERSION` | `ghcr.io/nixys/nxs-anomaly`, `…/nxs-anomaly-frontend` |
| `appVersion` chart-а | `v$VERSION` | `image.tag` по умолчанию равен ему, то есть это *и есть* тег, который разрешит рендер |
| `version` chart-а | `$VERSION` | версии chart-а обязаны быть SemVer, поэтому без `v` |

Все они происходят из [`VERSION`](../../../VERSION) в корне репозитория:
`scripts/set-version.sh` проставляет их, а `scripts/check-version.sh` (pre-commit,
pre-push и CI-джоба `test:version`) отклоняет релиз, где они расходятся. Потеря `v`
в `appVersion` — не косметика: из-за неё рендер укажет на тег образа, который никогда
не публиковался.

Пресет [`values-production.yaml`](values-production.yaml) включает production-профиль
безопасности (SSRF-гейт, лимиты частоты, circuit breaker, запрет inline-секретов),
NetworkPolicy, ServiceMonitor и PrometheusRule и требует внешнюю базу и Secret,
управляемый оператором. **Preflight** роняет рендер, если production-профиль
скомбинирован с inline-секретами, встроенной базой или ослабленным флагом
SSRF/secure-cookie: наполовину переведённая в production инсталляция останавливается
громко, а не уезжает небезопасной.

### Валидация до рендера

Два независимых механизма, и они дополняют друг друга:

- [`values.schema.json`](values.schema.json) — форма и типы. Helm проверяет его
  на `install`/`upgrade`/`lint`, поэтому `sslmode: bogus` или строка вместо
  числа реплик отклоняются раньше, чем шаблон начнёт рендериться.
- **Preflight** в `templates/_helpers.tpl` — сочетания, которые схема выразить
  не может: два одновременно включённых источника секретов, production с
  встроенным PostgreSQL, пустой хост внешней базы там, где чарт сам собирает
  DSN, число реплик (и PDB, который не даёт слить ни один под), трейсинг без
  endpoint, невыставленный team scoping, небезопасный `sslmode`, NetworkPolicy
  с ServiceMonitor, но без доступа Prometheus.

Схема отклоняет значение неправильного вида, preflight — правильное значение в
неправильном сочетании; сообщение об ошибке во втором случае объясняет, чем это
плохо, а не только что это запрещено.

## Проверьте подписи перед установкой

Каждый релиз по тегу подписывает и образы, **и** chart через cosign ключевой парой
проекта (та же модель, что в
[nxs-universal-chart](https://github.com/apps/nxs-universal-chart)). Публичная
половина публикуется с каждым релизом как артефакт `cosign.pub` джобы
`release:verify` (закрепите собственную копию — ключ, полученный оттуда же, откуда и
артефакт, за который он ручается, сам по себе доказывает мало):

```bash
VERSION=0.1.95
HELM_PROJECT="<значение HARBOR_HELM_PROJECT>"
IMAGE_PROJECT="<значение HARBOR_PROJECT>"

# У проекта с chart-ами свои учётные данные.
echo "$HELM_REGISTRY_PASSWORD" | docker login ghcr.io/nixys \
  -u "$HELM_REGISTRY_USER" --password-stdin
cosign verify --key cosign.pub \
  "ghcr.io/nixys/nxs-anomaly:$VERSION"
docker logout ghcr.io/nixys

# Образы приложения используют учётные данные проекта образов.
echo "$CI_REGISTRY_PASSWORD" | docker login ghcr.io/nixys \
  -u "$CI_REGISTRY_USER" --password-stdin
for ref in ghcr.io/nixys/nxs-anomaly:v$VERSION \
           ghcr.io/nixys/nxs-anomaly-frontend:v$VERSION; do
  cosign verify --key cosign.pub "$ref"
done
# SBOM образов приложены как cosign-аттестации:
cosign verify-attestation --key cosign.pub --type cyclonedx \
  ghcr.io/nixys/nxs-anomaly:v$VERSION
docker logout ghcr.io/nixys
```

Обратите внимание на асимметрию ссылок: chart адресуется своим SemVer (`$VERSION`),
образы — git-тегом (`v$VERSION`); см. таблицу версий выше.

**Почему ключ, а не keyless.** Keyless-cosign выпускает сертификат Fulcio от
OIDC-issuer-а CI, а публичный Sigstore доверяет `gitlab.com`, но не self-hosted
инстансу. CI этого проекта — `github.com`, поэтому keyless-подпись либо не была бы
выпущена, либо не проверялась бы против публичного корня доверия. Релизный ключ живёт
в маскированных переменных CI (`COSIGN_PRIVATE_KEY` / `COSIGN_PUBLIC_KEY`) и
подписывает **по digest**, поэтому тег, позже переставленный на другой артефакт,
подпись не наследует.

Эти команды не нужно принимать на веру. Релизный пайплайн выполняет ровно их против
только что опубликованных артефактов: chart забирается с `HELM_REGISTRY_USER` /
`HELM_REGISTRY_PASSWORD`, затем эти учётные данные удаляются, и только после этого
проверяются публичные образы приложения. Протокол публикуется как артефакт джобы
`release-verification.txt`.

Чтобы обеспечить это на уровне кластера, допускайте только подписанные образы через
контроллер политик (например, Kyverno `verifyImages` или Sigstore policy-controller)
с тем же публичным ключом.

### Как публикуется релиз

Пять джоб в четырёх стадиях, по одной ответственности на каждую — чтобы падение
называло конкретный шаг:

| Стадия | Джоба | Что делает |
|---|---|---|
| package | `release:sbom` | CycloneDX SBOM на каждый образ (syft) |
| package | `release:chart:package` | `helm package` с версией тега → артефакт `dist/*.tgz` |
| publish | `release:chart:publish` | `helm push` в `oci://$HARBOR_REGISTRY/$HARBOR_HELM_PROJECT`, фиксирует digest |
| sign | `release:sign` | `cosign sign --key` для обоих образов и chart-а (по digest) + аттестации SBOM |
| verify | `release:verify` | проверка chart-а с Helm-учётными данными, затем проверка образов с учётными данными образов |

Переменные CI, нужные релизу: `COSIGN_PRIVATE_KEY`, `COSIGN_PUBLIC_KEY` (плюс
`COSIGN_PASSWORD`, если ключ защищён паролем), `HARBOR_PROJECT`,
`CI_REGISTRY_USER` / `CI_REGISTRY_PASSWORD` для образов, а также
`HARBOR_HELM_PROJECT`, `HELM_REGISTRY_USER` и `HELM_REGISTRY_PASSWORD` для chart-а —
как генерируется и хранится пара, описано в
[CONTRIBUTING.md](../../../CONTRIBUTING.md#generating-the-release-key-pair).

## Обновления

```bash
VERSION=0.1.95          # релиз, на который обновляемся
HELM_PROJECT="<значение HARBOR_HELM_PROJECT>"
# Сначала посмотрите, что изменится.
helm diff upgrade nxs-anomaly "oci://ghcr.io/nixys/nxs-anomaly" \
  --version "$VERSION" -f values-production.yaml --reuse-values   # опциональный плагин
helm upgrade nxs-anomaly "oci://ghcr.io/nixys/nxs-anomaly" \
  --version "$VERSION" -f values-production.yaml --reuse-values
```

Миграции применяются автоматически при старте под advisory-локом (отдельной Job нет).
Сначала снимите резервную копию базы и убедитесь, что знаете путь отката — см.
[docs/BACKUP_RESTORE.md](../../../docs/BACKUP_RESTORE.md).

## Компоненты

| Компонент | Workload | Порт | Масштабирование |
|---|---|---|---|
| API | Deployment (`serve --no-scheduler`) | 8080 | горизонтальное |
| Worker | Deployment (`run-worker`) | 8081 (телеметрия) | несколько реплик безопасны (claim + шарды) |
| Frontend | Deployment (nginx SPA + same-origin прокси к API) | 8080 | горизонтальное |

Миграции применяются автоматически при старте под advisory-локом PostgreSQL, поэтому
параллельный старт реплик безопасен и **отдельной Job для миграций нет**.

## Секреты — выберите один основной источник

Secret с окружением приложения обязан содержать как минимум `NXS_ANOMALY_DB_DSN`.
Chart по умолчанию не зашивает секреты в открытом виде; рендер без включённого
источника падает.

| Режим | Values | Что делает |
|---|---|---|
| Существующий | `existingSecret.enabled`, `existingSecret.name` | ссылается на Secret, управляемый оператором |
| External Secrets | `externalSecrets.enabled`, `externalSecrets.secretStoreRef`, `externalSecrets.data` | рендерит `ExternalSecret` (external-secrets.io) |
| Vault Secrets Operator | `vaultSecretOperator.enabled`, `vaultSecretOperator.mount/path` | рендерит `VaultStaticSecret` |
| Inline (только dev) | `inlineSecret.enabled`, `inlineSecret.data` | рендерит Secret из values; DSN подставляется из встроенного или внешнего Postgres |

Аутентификация в режиме VSO задаётся одним из двух способов, вместе они
отвергаются preflight-ом:

* `vaultSecretOperator.vaultAuthRef` — имя `VaultAuth`, созданного платформой;
* `vaultSecretOperator.vaultAuth.create=true` — chart рендерит свой `VaultAuth`
  (`mount` — путь auth-бэкенда, не KV-mount; `vaultConnectionRef` — существующий
  `VaultConnection`; `kubernetes.role`, `kubernetes.serviceAccount` — по умолчанию
  ServiceAccount чарта, `kubernetes.audiences`). Тогда `VaultStaticSecret`
  ссылается на него автоматически.

`VaultConnection` chart не создаёт: адрес Vault, CA и TLS — инфраструктура
кластера, переживающая любой релиз. Если не указать ни `vaultAuthRef`, ни
`vaultAuth.create`, VSO молча использует свои default-объекты в namespace
оператора — это не ошибка установки, а тихо не обновляющийся секрет.

## Сервисы данных

Каждый может работать **встроенным** (одноузловой StatefulSet, только для
разработки и тестов) либо указывать на **внешний** managed-инстанс.

| Сервис | Встроенный | Внешний | Используется приложением |
|---|---|---|---|
| PostgreSQL | `postgresql.enabled` | `externalPostgres.*` (или DSN в секрете) | да (обязателен) |

## Сеть и наблюдаемость

- `ingress.enabled` — опубликовать фронтенд (который проксирует API same-origin).
- `ingress.name` — имя `Ingress`; по умолчанию — полное имя релиза.
- `istio.virtualService.enabled` — отрендерить Istio `VirtualService`. Привяжите его
  к существующему Gateway через `istio.virtualService.gateways` либо задайте
  `istio.gateway.enabled=true` и укажите `istio.gateway.name`; опциональный TLS
  использует `istio.gateway.tls.credentialName`.
- `istio.virtualService.name` — имя `VirtualService`; по умолчанию — полное имя
  релиза.
- `gatewayAPI.httpRoute.enabled` — отрендерить `HTTPRoute`
  (`gateway.networking.k8s.io/v1`). Привяжите через `gatewayAPI.httpRoute.parentRefs`
  либо позвольте chart-у создать Gateway через `gatewayAPI.gateway.enabled=true`,
  `name` и `gatewayClassName`. Маршрут между namespace-ами автоматически получает
  нужный backend `ReferenceGrant`.
- `gatewayAPI.httpRoute.name` — имя `HTTPRoute`; по умолчанию — полное имя релиза.
  `ReferenceGrant` для меж-namespace маршрута называется по этому же имени.
- Ingress, Istio и Gateway API отключаются независимо. Обычно включают ровно один;
  все три маршрутизируют `/` на same-origin прокси фронтенда либо напрямую в API,
  если `frontend.enabled=false`.

Имя задаётся у всех трёх маршрутов — `ingress.name`, `istio.virtualService.name`,
`gatewayAPI.httpRoute.name` — и по одной причине. Два релиза, публикующиеся через
один контроллер или кладущие маршруты в один namespace (community и enterprise
рядом, или по релизу на команду), дают одинаковое имя объекта, и GitOps-контроллер
видит один объект, принадлежащий двум приложениям: `VirtualService/nxs-anomaly is
part of applications argocd/nxs-anomaly-ce-team-x and nxs-anomaly-ee-team-x`.
**Переименование — не косметика:** старый объект удаляется, новый создаётся, и на
это время маршрут пропадает; меняйте имя в окно обслуживания.
- `networkPolicy.enabled` — default-deny плюс минимальные разрешения (DNS, внутри
  релиза, вход в API, исходящий трафик приложения). В остальном API и worker
  недостижимы извне релиза — включая Prometheus. Задайте
  `networkPolicy.extraIngress` тем, за чем живёт ваш Prometheus (обычно
  `namespaceSelector` по его namespace); production-preflight отказывается
  рендериться при `networkPolicy.enabled` + `serviceMonitor.enabled` без
  `extraIngress`, поскольку эта комбинация молча делает ServiceMonitor неспособным
  снимать метрики.
- `serviceMonitor.enabled` — ServiceMonitor для `/metrics` API и worker-а.
- `prometheusRule.enabled` — правила алертов BETA-021 (`files/alerting-rules.yaml`),
  включая группу burn-rate по бюджету ошибок (`nxs-anomaly.slo`).
- `tracing.enabled` + `tracing.endpoint` — трассировка OpenTelemetry в
  OTLP/HTTP-коллектор, сразу для API и worker-а. Включение без указания endpoint —
  это ошибка рендера, а не инсталляция, которая молча экспортирует в никуда. См.
  [docs/TRACING.md](../../../docs/TRACING.md).
- `rateLimits.webhookRatePerCluster` / `rateLimits.apiRatePerCluster` — общекластерные
  частоты приёма и обращений к API. Эти два лимитера держат корзины в каждом процессе
  API, поэтому chart делит указанную величину на `api.replicaCount` до того, как её
  увидит приложение; иначе масштабирование API молча масштабировало бы и лимиты.
  Лимитера входа здесь нет — он живёт в PostgreSQL именно затем, чтобы быть
  глобальным. См. [docs/SECURITY_PROFILE.md](../../../docs/SECURITY_PROFILE.md).
- По компонентам: `resources`, `podDisruptionBudget`, `topologySpreadConstraints`,
  `nodeSelector`/`affinity`/`tolerations`.

## Тесты

```bash
helm lint deploy/helm/nxs-anomaly --set inlineSecret.enabled=true
helm unittest -f 'tests/unit/*_test.yaml' deploy/helm/nxs-anomaly
# Настоящая install + upgrade проверка на одноразовом kind-кластере (нужны docker+kind):
bash deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh
# Проверка реального применения NetworkPolicy (ставит Calico — kindnet
# NetworkPolicy не применяет вовсе — и доказывает, что под из разрешённого
# namespace достаёт до API/worker, а посторонний namespace заблокирован):
bash deploy/helm/nxs-anomaly/tests/e2e/kind-networkpolicy.sh
```

`KEEP_CLUSTER=1` оставляет kind-кластер поднятым для разбора после любой из них.

### Post-install acceptance

`tests.acceptance.enabled=true` добавляет второй `helm test`-под, который
отвечает на вопрос, ради которого установка вообще делалась: если сейчас придёт
алерт, разбудят ли кого-нибудь. Он заводит временную canary-интеграцию,
отправляет через неё настоящий алерт, дожидается **попытки доставки** (это
единственное ожидание, которое проверяет воркер, а не API — уведомление,
навсегда застрявшее в `delivery_scheduled`, для `/health` выглядит здоровым),
делает acknowledge и resolve, проверяет, что оба попали в аудит, и удаляет за
собой всё созданное.

Две детали не случайны. Canary-дежурный настроен на канал `log`: это настоящий
канал, проходящий весь конвейер доставки и оставляющий настоящую запись о
попытке, но не требующий ни egress, ни исключения в SSRF-гейте — иначе провал
теста сообщал бы мнение сети, а не состояние инсталляции. И уборка висит на
`trap`, а не в конце happy path: тест, упавший на середине, иначе оставил бы на
проде живой ingest-эндпоинт.

Нужен админский API-ключ в Secret приложения (по умолчанию ключ
`NXS_ANOMALY_ACCEPTANCE_API_KEY`) — он читается оттуда, а не из values, чтобы не
попасть в манифест релиза. Выключен по умолчанию: тест пишет в инсталляцию.

```bash
helm test <release> --filter name=<release>-nxs-anomaly-acceptance
```

Все три идут в CI на каждом теге (`helm:kind`, `helm:kind-networkpolicy`,
подъём kind-кластера не укладываются в стандартный 20-минутный таймаут джобы,
поэтому у каждой свой, — и это единственное место, где настоящая установка,
Прогнать одну локально перед пушем обычно быстрее, чем дожидаться тега.
