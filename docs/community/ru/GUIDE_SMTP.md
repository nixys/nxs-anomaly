# Доставка почты через SMTP

## Перед началом

Получите адрес релея, порт, режим TLS, разрешённый адрес отправителя и учётные данные. Разрешите соединения от API и воркера. Для частного сертификата установите CA релея. [Способы установки](INSTALLATION.md).

## Подключение и настройка

1. Передайте параметры ниже в окружение приложения, пароль — через секрет.
2. Для STARTTLS укажите порт submission релея и выставьте USE_TLS в false.
3. Заполните email пользователя и включите email в notification_targets.

```dotenv
NXS_ANOMALY_SMTP_HOST=smtp.example.org
NXS_ANOMALY_SMTP_PORT=465
NXS_ANOMALY_SMTP_USE_TLS=true
NXS_ANOMALY_SMTP_USERNAME=oncall
NXS_ANOMALY_SMTP_SENDER=oncall@example.org
```

Передайте параметры через окружение сервиса. В Helm используйте extraConfig для открытых значений и Secret приложения для ключей. После изменения окружения пересоздайте соответствующие процессы API и воркера.

Для проверки адаптера с правами администратора задайте в оболочке API, API_KEY и USER_ID и выполните команду.

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"email"}'
```

## Контрольная проверка

Для релея с implicit TLS сначала проверьте TLS из сети доставки. Для порта submission с STARTTLS добавьте -starttls smtp и укажите нужный порт. Проверьте цепочку сертификатов и имя хоста.

```bash
openssl s_client -connect smtp.example.org:465 -servername smtp.example.org \
  -verify_hostname smtp.example.org -verify_return_error </dev/null
```

## Проверка и устранение неполадок

Отправьте тест из профиля и проверьте попытку доставки и почтовый ящик. Если релей принял письмо, а ящик пуст, проверьте очередь релея и спам-фильтр. При отказе авторизации проверьте `NXS_ANOMALY_SMTP_PASSWORD` и разрешённый адрес From.

[Порты, учётные данные и отправитель SMTP](CONFIGURATION.md#почта-smtp) · [Каталог интеграций](INTEGRATIONS.md)
