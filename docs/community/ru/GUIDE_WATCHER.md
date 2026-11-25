# Elasticsearch Watcher

## Подготовка установки

Нужны работающие API и воркер nxs-anomaly, права редактора, тестовый получатель и доступ к настройкам отправителя. Из сети отправителя проверьте публичный HTTPS-адрес и сертификат. [Установка nxs-anomaly](INSTALLATION.md). [Установка Elasticsearch](https://www.elastic.co/docs/deploy-manage/deploy/self-managed/installing-elasticsearch).

## Подключение отправителя

1. В nxs-anomaly создайте цепочку с уведомлением тестового пользователя. В разделе «Интеграции» создайте тестовую интеграцию типа `elasticsearch`, назначьте цепочку по умолчанию и скопируйте ключ.
2. Добавьте в watch действие webhook с методом POST. Разнесите URL по полям scheme, host, port и path и передайте JSON с `watch_id` из ctx.watch_id. Предусмотрите отдельное действие восстановления с тем же ID и status resolved.
3. В тестовой оболочке задайте API с базовым адресом установки и KEY с ключом интеграции. Выполните запрос ниже из сети отправителя.

Для настройки отправителя адаптируйте пример под адрес и ключ назначения. Поместите объект в именованный элемент actions вашего watch.

```json
{
  "webhook": {
    "scheme": "https",
    "host": "oncall.example.org",
    "port": 443,
    "method": "post",
    "path": "/integrations/v1/elasticsearch/REPLACE_KEY",
    "headers": {
      "Content-Type": "application/json"
    },
    "body": "{\"watch_id\":\"{{ctx.watch_id}}\",\"execution_time\":\"{{ctx.execution_time}}\",\"status\":\"firing\"}"
  }
}
```

Проверочный запрос к API nxs-anomaly

```bash
curl --fail-with-body -i -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"watch_id":"checkout-latency","status":"firing","metadata":{"severity":"warning","team":"checkout"}}'
```

## Проверка жизненного цикла

Выполните watch с тестовыми данными, проверьте результат действия и вызовите восстановление. Если алерты двух watch объединяются, сравните `watch_id` и настройки группировки интеграции. В истории группы проверьте выполнение выбранной цепочки и сопоставьте попытку доставки с уведомлением у адресата.

После подтверждения доставки закройте тестовый инцидент соответствующим событием восстановления.

```bash
curl --fail-with-body -X POST "$API/integrations/v1/elasticsearch/$KEY" \
  -H 'Content-Type: application/json' \
  --data-binary '{"watch_id":"checkout-latency","status":"resolved","metadata":{"severity":"warning","team":"checkout"}}'
```

## Эксплуатация и диагностика

При неверной группировке или отсутствии доставки сравните поля и метки события с предпросмотром маршрута. При 404 проверьте полный путь и действующий ключ. При 503 учитывайте Retry-After. После ротации ключа сразу обновите отправитель и повторите тест. [Маршруты и уведомления](CONFIGURATION.md) · [Преобразование полей](ALERT_PROCESSING.md) · [Ответы HTTP](API.md).

[Каталог интеграций](INTEGRATIONS.md)
