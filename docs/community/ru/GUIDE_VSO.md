# Vault Secrets Operator

## Перед началом

Установите HashiCorp Vault Secrets Operator, настройте VaultConnection и VaultAuth в нужном namespace. Дайте роли чтение пути KV с ключами, названными как переменные окружения приложения, включая NXS_ANOMALY_DB_DSN. [Способы установки](INSTALLATION.md). [Установка Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/vso/installation).

## Подключение и настройка

1. Включите vaultSecretOperator как единственный основной источник и укажите существующие mount, path и ссылку на VaultAuth.
2. Примените релиз.
3. Проверьте созданный чартом VaultStaticSecret и целевой Secret.

```yaml
vaultSecretOperator:
  enabled: true
  mount: kv
  type: kv-v2
  path: nxs-anomaly
  vaultAuthRef: app-auth
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Проверьте ресурс синхронизации и узнайте имя целевого секрета. События оператора помогут отличить отказ авторизации от ошибки пути KV.

```bash
kubectl -n "$NS" get vaultauth,vaultstaticsecret
kubectl -n "$NS" describe vaultstaticsecret "$NAME"
kubectl -n "$NS" get vaultstaticsecret "$NAME" -o jsonpath='{.spec.destination.name}'
```

## Проверка и устранение неполадок

Проверьте kubectl describe vaultstaticsecret и события оператора. При permission denied сверьте политику Vault, привязку роли и тип mount. После ротации дождитесь refreshAfter, перезапустите потребителей переменных окружения и проверьте доступ.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
