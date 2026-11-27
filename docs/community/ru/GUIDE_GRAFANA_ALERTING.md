# Grafana Alerting

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте публичный HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md). [Настройка точек контакта Webhook](https://grafana.com/docs/grafana/latest/alerting/configure-notifications/manage-contact-points/integrations/webhook-notifier/).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `grafana-alerting`, назначьте цепочку по умолчанию и скопируйте ключ.
2. В Grafana Alerting создайте точку контакта Webhook, выберите POST и укажите адрес ниже. Оставьте стандартное тело webhook и включите уведомления о восстановлении. Назначьте точку правилу или подходящей политике уведомлений.
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/grafana-alerting/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"receiver":"nxs","alerts":[{"status":"firing","fingerprint":"grafana-test","labels":{"alertname":"CheckoutLatency"},"annotations":{"summary":"Checkout latency"}}]}'
```

## Проверка жизненного цикла

Сначала нажмите Test у точки контакта, затем вызовите и снимите условие тестового правила. При появлении второй группы сравните alerts[] и fingerprint. Пустой массив alerts отклоняется. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/grafana-alerting/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"receiver":"nxs","alerts":[{"status":"resolved","fingerprint":"grafana-test","labels":{"alertname":"CheckoutLatency"},"annotations":{"summary":"Checkout latency"}}]}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
