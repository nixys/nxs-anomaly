# VictorOps / Splunk On-Call

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте публичный HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `victorops`, назначьте цепочку по умолчанию и скопируйте ключ.
2. Укажите адрес ниже в отправителе формата VictorOps. Передайте идентификатор объекта в `entity_id`, а тип события в message_type. В действии восстановления сохраните тот же ID объекта.
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/victorops/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"message_type":"CRITICAL","entity_id":"checkout-test","entity_display_name":"Checkout latency","state_message":"Test notification"}'
```

## Проверка жизненного цикла

Отправьте ACKNOWLEDGEMENT, затем RECOVERY с исходным entity_id. Убедитесь, что открытая группа подтверждена и закрыта. Успешный запрос возвращает HTTP 200. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/victorops/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"message_type":"RECOVERY","entity_id":"checkout-test","entity_display_name":"Checkout latency","state_message":"Test notification"}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
