# Уведомления и команды Slack

## Перед началом

Установите приложение Slack в рабочее пространство, включите incoming webhooks и создайте webhook нужного канала. Сохраните ID канала и signing secret приложения. Проверьте доступность публичного HTTPS API из Slack. [Способы установки](INSTALLATION.md). [Создание и установка входящего webhook](https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/).

## Подключение и настройка

1. Создайте канал Slack в ChatOps с полями ниже, командой и внешним ID канала.
2. Задайте `NXS_ANOMALY_SLACK_SIGNING_SECRET`, зарегистрируйте каждую slash-команду по пути /integrations/v1/chatops/slack, а для кнопок укажите /integrations/v1/chatops/slack/interactive на том же хосте.
3. Привяжите `slack_id` пользователя.

```json
{
  "platform": "slack",
  "name": "checkout-alerts",
  "external_id": "C01234567",
  "webhook_url": "https://hooks.slack.com/services/REPLACE",
  "notifications_enabled": true,
  "commands_enabled": true,
  "team_id": "REPLACE_TEAM_ID"
}
```

Создайте канал через POST /api/v1/chatops/channels или экран ChatOps. Добавьте получателя в привязанную команду. Назначьте интеграции цепочку с NOTIFY_USER или NOTIFY_TEAM, чтобы уведомление получал хотя бы один участник.

## Проверка и устранение неполадок

Направьте тестовый алерт в канал, вызовите зарегистрированную команду и проверьте автора действия в истории группы. Если отправка работает, а команды нет, проверьте адрес запроса, часы сервера и signing secret. После отзыва webhook обновите URL канала.

[Обратные запросы ChatOps](API.md) · [Каталог интеграций](INTEGRATIONS.md)
