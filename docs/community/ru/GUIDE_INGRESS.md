# Публикация через Kubernetes Ingress

## Перед началом

Установите ingress-контроллер с известным IngressClass. Направьте DNS-имя сервиса на внешний адрес контроллера и создайте TLS Secret с подходящим сертификатом в namespace релиза. [Способы установки](INSTALLATION.md). [Выбор Ingress-контроллера](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/).

## Подключение и настройка

1. Включите ingress в чарте, задайте класс, hostname и TLS Secret по примеру.
2. Оставьте frontend включённым для маршрута чарта и разрешите трафик от контроллера в NetworkPolicy.
3. Примените релиз.

```yaml
ingress:
  enabled: true
  className: nginx
  host: oncall.example.org
  tls:
    - hosts: [oncall.example.org]
      secretName: oncall-tls
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Задайте публичное имя в HOST и проверьте, что HTTPS-запрос доходит до приложения и БД.

```bash
kubectl -n "$NS" get ingress,service,endpointslice
curl --fail-with-body "https://$HOST/health"
```

## Проверка и устранение неполадок

Проверьте kubectl get ingress и откройте интерфейс по HTTPS. Проверьте API на том же хосте тестовым webhook. При default backend или 404 сравните Host запроса, IngressClass и сервис backend. Обновляйте сертификат до истечения срока.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
