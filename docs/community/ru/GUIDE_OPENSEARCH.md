# OpenSearch Alerting

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте публичный HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md). [Установка OpenSearch](https://docs.opensearch.org/latest/install-and-configure/).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `opensearch`, назначьте цепочку по умолчанию и скопируйте ключ.
2. Создайте канал custom webhook с POST и application/json. Привяжите действие к триггеру монитора. Возьмите структуру ниже и подставляйте идентификаторы монитора и триггера из контекста действия. Для завершения добавьте действие со status resolved. [Действия OpenSearch](https://docs.opensearch.org/latest/observing-your-data/alerting/actions/).
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

Для настройки отправителя адаптируйте пример под адрес и ключ назначения.

```json
{"monitor":{"id":"{{ctx.monitor._id}}","name":"{{ctx.monitor.name}}"},"trigger":{"id":"{{ctx.trigger._id}}","name":"{{ctx.trigger.name}}","severity":"{{ctx.trigger.severity}}"},"status":"firing"}
```

Проверочный запрос к API nxs-anomaly

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/opensearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"monitor":{"id":"checkout","name":"Checkout"},"trigger":{"id":"slow","name":"Latency","severity":"2"},"status":"firing"}'
```

## Проверка жизненного цикла

Запустите монитор на тестовом индексе и снимите условие. Сверьте метки monitor и trigger. Для bucket-монитора сохраняйте `bucket_keys` при восстановлении. При 400 проверьте JSON после подстановки значений. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/opensearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"monitor":{"id":"checkout","name":"Checkout"},"trigger":{"id":"slow","name":"Latency","severity":"2"},"status":"resolved"}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
