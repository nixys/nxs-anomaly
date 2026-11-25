# Правила Kibana

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте публичный HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md). [Установка Kibana](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/install-kibana).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `elasticsearch`, назначьте цепочку по умолчанию и скопируйте ключ.
2. Создайте коннектор Webhook для POST с JSON по адресу ниже. Назначьте его действиям срабатывания и восстановления правила. Подставляйте rule.id, rule.name и alert.id из контекста. Для действия восстановления задайте status recovered.
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

Для настройки отправителя адаптируйте пример под адрес и ключ назначения.

```json
{"rule":{"id":"{{rule.id}}","name":"{{rule.name}}"},"alert":{"id":"{{alert.id}}"},"status":"{{alert.actionGroup}}","message":"{{context.message}}"}
```

Проверочный запрос к API nxs-anomaly

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"rule":{"id":"checkout","name":"Checkout"},"alert":{"id":"latency","actionGroup":"active"},"message":"High latency"}'
```

## Проверка жизненного цикла

Вызовите тестовое срабатывание и восстановление. Если инцидент остался открытым, сравните rule.id и alert.id обоих запросов. Когда запросы не доходят до сервиса, проверьте журнал выполнения коннектора. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"rule":{"id":"checkout","name":"Checkout"},"alert":{"id":"latency","actionGroup":"recovered"},"message":"High latency"}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
