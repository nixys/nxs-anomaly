# Уведомления и команды Mattermost

## Перед началом

Создайте в Mattermost входящий webhook для канала команды и получите ID канала. Разрешите Mattermost отправлять POST на публичный HTTPS-адрес nxs-anomaly. [Способы установки](INSTALLATION.md). [Включение входящих webhook](https://developers.mattermost.com/integrate/webhooks/incoming/).

## Подключение и настройка

1. Создайте канал Mattermost в ChatOps по примеру ниже.
2. Для кнопок задайте `NXS_ANOMALY_PUBLIC_URL` и NXS_ANOMALY_MATTERMOST_ACTION_SECRET.
3. Зарегистрируйте slash-команду POST по пути /integrations/v1/chatops/mattermost/command и сохраните выданный токен в NXS_ANOMALY_MATTERMOST_COMMAND_TOKEN.
4. Привяжите `mattermost_id` пользователя.

```json
{
  "platform": "mattermost",
  "name": "checkout-alerts",
  "external_id": "REPLACE_CHANNEL_ID",
  "webhook_url": "https://chat.example.org/hooks/REPLACE",
  "notifications_enabled": true,
  "commands_enabled": true,
  "team_id": "REPLACE_TEAM_ID"
}
```

Создайте канал через POST /api/v1/chatops/channels или экран ChatOps. Добавьте получателя в привязанную команду. Назначьте интеграции цепочку с NOTIFY_USER или NOTIFY_TEAM, чтобы уведомление получал хотя бы один участник.

## Проверка и устранение неполадок

Отправьте тестовый алерт, подтвердите его кнопкой и вызовите зарегистрированную команду с аргументом help. При отсутствии кнопок проверьте публичный URL и секрет действий. При 501 от команды проверьте отдельный токен команды. После ротации повторите обе проверки.

[Настройка бота Mattermost](CONFIGURATION.md#добавление-mattermost-бота) · [Каталог интеграций](INTEGRATIONS.md)
