# Сбор метрик API и воркера в Prometheus

## Перед началом

Подготовьте Prometheus с доступом к портам метрик API и воркера. Для Kubernetes установите Prometheus Operator и разрешите Prometheus выбирать мониторы и правила в namespace релиза. [Способы установки](INSTALLATION.md). [Установка Prometheus Operator](https://prometheus-operator.dev/docs/getting-started/installation/).

## Подключение и настройка

1. Включите ресурсы чарта из примера ниже.
2. Добавьте метки, требуемые селекторами Prometheus, и разрешите трафик через NetworkPolicy.

Для Prometheus без Operator задайте адреса API и воркера в `scrape_configs`. Prometheus опрашивает путь `/metrics` по умолчанию. Сохраните пример с верхним ключом `groups` в отдельном файле и укажите его путь в `rule_files`.

```yaml
serviceMonitor:
  enabled: true
prometheusRule:
  enabled: true
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Проверка и устранение неполадок

Проверьте UP для обеих целей, выполните запрос up для каждой и откройте список правил Prometheus. Если после обновления обе цели исчезли, проверьте селекторы ServiceMonitor и namespace. При DOWN сверьте порт сервиса и NetworkPolicy.

[Правила и настройки сбора](ALERTING_RULES.md) · [Каталог интеграций](INTEGRATIONS.md)
