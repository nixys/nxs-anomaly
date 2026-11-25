# Каталог интеграций

## Приём алертов

Создайте интеграцию, укажите цепочку эскалации и скопируйте URL с ключом в настройки отправителя. Выберите обработчик по формату сообщения. Все перечисленные маршруты принимают HTTP POST. [Настройка маршрутизации](CONFIGURATION.md#маршруты) · [Справочник API](API.md#приём-алертов).

| Отправитель или формат | Куда отправлять |
|---|---|
| [Универсальный JSON webhook](GUIDE_WEBHOOK.md) | `/integrations/v1/webhook/{key}`. Для повторной отправки одного события передавайте тот же `nxs_event_id`. |
| [Prometheus Alertmanager](GUIDE_ALERTMANAGER.md) | `/integrations/v1/alertmanager/{key}` для webhook-получателя Alertmanager |
| [Grafana Alerting](GUIDE_GRAFANA_ALERTING.md) | `/integrations/v1/grafana-alerting/{key}` для точки контакта типа Webhook |
| [PagerDuty Events v2](GUIDE_PAGERDUTY.md) | `/integrations/v1/pagerduty/{key}` для сообщений в формате Events v2 |
| [VictorOps / Splunk On-Call](GUIDE_VICTOROPS.md) | `/integrations/v1/victorops/{key}` |
| [OpenSearch Alerting](GUIDE_OPENSEARCH.md) | `/integrations/v1/opensearch/{key}` для шаблона сообщения канала |
| [Правила Kibana](GUIDE_KIBANA.md) · [Elasticsearch Watcher](GUIDE_WATCHER.md) | `/integrations/v1/elasticsearch/{key}` |
| [Legacy-пул NXS](GUIDE_LEGACY.md) | `/v2/alert/pool`. Ключ передаётся в заголовке `X-Auth-Key`. |
| [OpenCVE](OPENCVE.md) | Укажите URL адаптера в OpenCVE. Адаптер преобразует пакет CVE в события универсального webhook. [Настройка доставки в Telegram](OPENCVE.md). |

## Доставка и действия по алерту

| Что подключить | Как использовать |
|---|---|
| [Telegram](GUIDE_TELEGRAM.md) | Настройте токен бота для доставки в личный или общий чат. Для команд и кнопок зарегистрируйте webhook. [Добавление Telegram-бота](CONFIGURATION.md#добавление-telegram-бота). |
| [Slack](GUIDE_SLACK.md) · [Mattermost](GUIDE_MATTERMOST.md) | Укажите входящий webhook чата для доставки. Для команд и кнопок настройте обратные запросы и секреты соответствующей платформы. [ChatOps](CONFIGURATION.md#8-chatops). |
| [Доставка почты через SMTP](GUIDE_SMTP.md) | Подключите SMTP-сервер и добавьте канал `email` в личную политику уведомлений. [Настройка SMTP](CONFIGURATION.md#почта-smtp). |
| [Звонки через Asterisk AMI](GUIDE_ASTERISK.md) | Настройте Asterisk AMI и dialplan. Канал `call` инициирует звонок на номер получателя. |
| [Мобильный клиент и push-шлюз](GUIDE_MOBILE.md) | Привяжите устройство для получения событий через SSE. Для push-уведомлений задайте адрес своего шлюза в `NXS_ANOMALY_MOBILE_PUSH_URL`. [Мобильный клиент](UI.md). |
| [Исходящий HTTP webhook](GUIDE_OUTBOUND.md) | Шаг `TRIGGER_WEBHOOK` отправляет POST в формате Alertmanager. При необходимости задайте заголовки авторизации. [Шаги эскалации](CONFIGURATION.md#все-типы-шагов). |
| [Создание задач в Redmine](GUIDE_REDMINE.md) · [Универсальный HTTP-приёмник задач](GUIDE_ISSUES.md) | Шаг `CREATE_ISSUE` создаёт задачу в проекте Redmine. Универсальный обработчик отправляет JSON с полями `subject` и `body` на указанный URL. Для трекера с другой схемой понадобится адаптер. |
| [Оповещение об окончательных сбоях доставки](GUIDE_DEAD_LETTER.md) | Отдельный webhook получает событие после исчерпания попыток доставки. |

Для проверки отправьте тестовый алерт и откройте историю попыток доставки. Там видны выбранная цепочка, канал и ответ провайдера. [Проверка доставки](CONFIGURATION.md#тестовый-алерт-и-попытка-доставки).

## Автоматизация и эксплуатация

- [Terraform](GUIDE_TERRAFORM.md) · [REST API](GUIDE_REST.md). Управляйте ресурсами установки из Terraform или вызывайте API из собственного клиента. [Провайдер Terraform](https://github.com/nixys/terraform-provider-nxs-anomaly) · [Справочник API](API.md).
- [Prometheus](GUIDE_PROMETHEUS.md) · [Grafana](GUIDE_GRAFANA.md). Собирайте метрики сервиса с `/metrics`, подключите правила оповещения и импортируйте дашборд Grafana. В Kubernetes доступны ServiceMonitor и PrometheusRule. [Правила Prometheus](ALERTING_RULES.md) · [Дашборд Grafana](../../grafana-dashboard.json).
- [Экспорт трасс по OTLP/HTTP](GUIDE_OTEL.md). Экспортируйте трассы API и воркера в коллектор по OTLP/HTTP. [Настройка трассировки](TRACING.md).
- [PostgreSQL](GUIDE_POSTGRES.md) · [Kubernetes / Helm](GUIDE_KUBERNETES.md) · [External Secrets Operator](GUIDE_ESO.md) · [Vault Secrets Operator](GUIDE_VSO.md) · [Ingress](GUIDE_INGRESS.md) · [Istio](GUIDE_ISTIO.md) · [Gateway API](GUIDE_GATEWAY.md). Helm-чарт поддерживает внешнюю БД, получение секретов через External Secrets Operator или Vault Secrets Operator и публикацию через Ingress, Istio или Gateway API. [Развёртывание](DEPLOY.md) · [Параметры Helm](../../../deploy/helm/nxs-anomaly/README.md).
- [Прокси для доставки уведомлений](GUIDE_PROXY.md) · [Подключение частного центра сертификации](GUIDE_CA.md). Для исходящей доставки через корпоративную сеть настройте прокси и доверенные сертификаты. [Настройка сети](PROXY.md).
