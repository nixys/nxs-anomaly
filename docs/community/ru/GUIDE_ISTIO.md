# Маршрутизация через Istio

## Перед началом

Установите Istio с ingress gateway. Настройте listener, TLS credential и публичное DNS-имя. Узнайте namespace/name ресурса Gateway, который принимает имя приложения. [Способы установки](INSTALLATION.md). [Установка Istio](https://istio.io/latest/docs/setup/).

## Подключение и настройка

1. Включите VirtualService и привяжите его к существующему Gateway по примеру.
2. Согласуйте hosts с listener и DNS.
3. Примените изменения и проверьте адрес назначения в VirtualService.

```yaml
istio:
  virtualService:
    enabled: true
    gateways: [istio-system/public]
    hosts: [oncall.example.org]
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Настройте istioctl для кластера и проверьте отсутствие gateway, некорректные hosts и ссылки на сервисы перед изменением маршрутов.

```bash
istioctl analyze -n "$NS"
kubectl -n "$NS" get virtualservice -o wide
```

## Проверка и устранение неполадок

Откройте внешний URL и отправьте тестовый webhook через шлюз. При 404 проверьте hosts и привязку gateway, при 503 — endpoints назначения и политики mesh. После замены шлюза или сертификата повторите проверку.

[Справочник по развёртыванию](DEPLOY.md) · [Каталог интеграций](INTEGRATIONS.md)
