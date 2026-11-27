# Prometheus Alertmanager

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте доступный из этой сети HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md). [Обзор Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `alertmanager`, назначьте цепочку по умолчанию и скопируйте ключ.
2. Добавьте в alertmanager.yml получатель nxs с `webhook_configs`, адресом интеграции в url и `send_resolved`: true. Создайте дочерний маршрут для меток тестового алерта с receiver: nxs. Проверьте файл командой `amtool check-config alertmanager.yml` и перечитайте конфигурацию. [Конфигурация Alertmanager](https://prometheus.io/docs/alerting/latest/configuration/).
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

Для настройки отправителя адаптируйте пример под адрес и ключ назначения. Минимальная конфигурация направляет все алерты тестовому получателю. На существующей установке добавьте получателя и дочерний маршрут для тестовых меток в действующее дерево.

```yaml
route:
  receiver: nxs
receivers:
  - name: nxs
    webhook_configs:
      - url: https://oncall.example.org/integrations/v1/alertmanager/REPLACE_KEY
        send_resolved: true
```

Проверочный запрос к API nxs-anomaly

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"version":"4","receiver":"nxs","alerts":[{"status":"firing","labels":{"alertname":"CheckoutTest","service":"checkout"},"fingerprint":"checkout-test"}]}'
```

## Проверка жизненного цикла

Активируйте тестовое правило в Prometheus. Дождитесь состояния `firing` и первого уведомления от Alertmanager, затем снимите условие. Если алерт восстановится до истечения `group_wait`, Alertmanager может пропустить уведомление. Проверьте закрытие той же группы по fingerprint. Если прямой POST работает, а правило не приходит, проверьте порядок маршрутов, silences и inhibition в Alertmanager. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/alertmanager/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"version":"4","receiver":"nxs","alerts":[{"status":"resolved","labels":{"alertname":"CheckoutTest","service":"checkout"},"fingerprint":"checkout-test"}]}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
