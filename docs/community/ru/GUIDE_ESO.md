# External Secrets Operator

## Перед началом

Установите оператор с поддержкой external-secrets.io/v1beta1 и подготовьте Ready SecretStore или ClusterSecretStore с доступом к секрету приложения. Сохраните там DSN PostgreSQL и ключи провайдеров. [Способы установки](INSTALLATION.md). [Установка External Secrets Operator](https://external-secrets.io/latest/introduction/getting-started/).

## Подключение и настройка

1. Выберите externalSecrets единственным основным источником секрета в чарте.
2. Замените store, key и property в примере, добавьте соответствие для каждой переменной.
3. Примените релиз и дождитесь синхронизации перед подачей трафика.

```yaml
externalSecrets:
  enabled: true
  secretStoreRef: {name: app-store, kind: ClusterSecretStore}
  data:
    - secretKey: NXS_ANOMALY_DB_DSN
      remoteRef: {key: nxs-anomaly, property: db_dsn}
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Укажите namespace релиза и имя созданного ресурса. Проверьте наличие ключа DSN, не выводя его значение.

```bash
kubectl -n "$NS" get externalsecret
kubectl -n "$NS" describe externalsecret "$NAME"
kubectl -n "$NS" get secret "$NAME" -o json | jq -e '.data | has("NXS_ANOMALY_DB_DSN")'
```

## Проверка и устранение неполадок

Проверьте kubectl get externalsecret и условие Ready. Если целевой Secret отсутствует, проверьте права store и имена полей. После ротации проверьте ревизию Secret, перезапустите API и воркер для чтения окружения и проверьте /health.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
