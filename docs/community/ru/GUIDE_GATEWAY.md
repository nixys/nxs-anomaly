# Публикация через Kubernetes Gateway API

## Перед началом

Установите CRD Gateway API и контроллер, подготовьте Gateway с listener для имени сервиса. В allowedRoutes разрешите маршруты из namespace приложения. Настройте DNS и TLS на шлюзе. [Способы установки](INSTALLATION.md). [Установка Gateway API](https://gateway-api.sigs.k8s.io/guides/).

## Подключение и настройка

1. Включите HTTPRoute в чарте и укажите parentRefs и hostnames по примеру.
2. Для общего Gateway явно укажите namespace родителя.
3. Примените релиз и проверьте статус маршрута.

```yaml
gatewayAPI:
  httpRoute:
    enabled: true
    parentRefs: [{name: public, namespace: gateways}]
    hostnames: [oncall.example.org]
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Задайте ROUTE с именем HTTPRoute и NS с его namespace. На установках с несколькими привязками проверьте состояние каждого родительского ресурса.

```bash
kubectl -n "$NS" get httproute "$ROUTE" -o json | jq '.status.parents[] | {parentRef, conditions}'
```

## Проверка и устранение неполадок

Проверьте Accepted и ResolvedRefs в статусе маршрута и отправьте тестовый алерт по HTTPS. Если привязки нет, проверьте allowedRoutes и имя listener. При ошибке backend проверьте Service и ReferenceGrant для ссылок между namespace.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
