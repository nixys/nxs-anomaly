# Установка nxs-anomaly через Helm

## Перед началом

Подготовьте доступ kubectl к кластеру, Helm, чарт вашей редакции, namespace и PostgreSQL. Создайте Secret окружения в namespace с `NXS_ANOMALY_DB_DSN` и учётными данными приложения. [Способы установки](INSTALLATION.md). [Установка Helm](https://helm.sh/docs/intro/install/).

## Подключение и настройка

1. Из корня репозитория задайте RELEASE и NS для релиза и namespace.
2. В values.yaml включите existingSecret и укажите имя.
3. Настройте внешний доступ к frontend или используйте port-forward для локальной проверки.
4. Проверьте результат рендера и установите чарт.

```bash
helm template "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml
helm upgrade --install "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
helm test "$RELEASE" -n "$NS"
```

## Проверка и устранение неполадок

Проверьте readiness подов, /health и доставку одного тестового алерта через воркер. При ошибках планирования или монтирования откройте kubectl describe pod, при ошибках миграций и доступа — журналы API и воркера. Храните values в системе контроля версий.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
