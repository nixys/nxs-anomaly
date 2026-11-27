# Уведомления через Telegram-бота

## Перед началом

Создайте бота через BotFather. Попросите тестового адресата запустить бота, узнайте числовой Telegram ID и запишите `NXS_ANOMALY_TELEGRAM_BOT_TOKEN` в секрет приложения. Для общей группы добавьте бота и сохраните ID чата. [Способы установки](INSTALLATION.md). [Создание бота через BotFather](https://core.telegram.org/bots/features#creating-a-new-bot).

## Подключение и настройка

1. Заполните `telegram_id` пользователя и включите telegram в notification_targets.
2. Для кнопок задайте `NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET` и зарегистрируйте публичный HTTPS-адрес через setWebhook с этим секретом.
3. Общий чат команды привяжите как канал ChatOps Telegram.

```json
{"telegram_id":"123456789","notification_targets":["telegram"]}
```

Для проверки адаптера с правами администратора задайте в оболочке API, API_KEY и USER_ID и выполните команду.

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"telegram"}'
```

## Контрольная проверка

Задайте в API публичный HTTPS URL установки. Для командного чата укажите числовой ID чата в обоих полях канала ChatOps — name и external_id. После смены токена повторите регистрацию webhook, проверьте `pending_update_count` и last_error_message.

```bash
curl --fail-with-body -X POST "https://api.telegram.org/bot$NXS_ANOMALY_TELEGRAM_BOT_TOKEN/setWebhook" \
  --data-urlencode "url=$API/integrations/v1/chatops/telegram" \
  --data-urlencode "secret_token=$NXS_ANOMALY_TELEGRAM_WEBHOOK_SECRET"
curl --fail-with-body "https://api.telegram.org/bot$NXS_ANOMALY_TELEGRAM_BOT_TOKEN/getWebhookInfo"
```

## Проверка и устранение неполадок

Отправьте личное тестовое уведомление и подтвердите тестовую группу кнопкой бота. Если сообщения нет, проверьте ID и запуск бота пользователем. Если кнопка молчит, проверьте getWebhookInfo и секрет после ротации.

[Настройка и команды setWebhook](CONFIGURATION.md#добавление-telegram-бота) · [Каталог интеграций](INTEGRATIONS.md)
