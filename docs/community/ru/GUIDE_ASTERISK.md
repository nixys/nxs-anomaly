# Звонки через Asterisk AMI

## Перед началом

Создайте учётную запись AMI с правом Originate и подготовьте контекст и extension в dialplan для обработки алерта. Откройте TCP 5038 от воркера и API до АТС. Согласуйте тестовый номер с получателем. [Способы установки](INSTALLATION.md). [Установка и настройка Asterisk](https://docs.asterisk.org/Getting-Started/).

## Подключение и настройка

1. Передайте пароль AMI в `NXS_ANOMALY_ASTERISK_SECRET` и адаптируйте параметры ниже под АТС.
2. К префиксу CHANNEL приложение добавляет номер.
3. Сохраните номер в поле phone пользователя и включите канал call.

```dotenv
NXS_ANOMALY_ASTERISK_HOST=pbx.example.org
NXS_ANOMALY_ASTERISK_USERNAME=oncall
NXS_ANOMALY_ASTERISK_CHANNEL=PJSIP/
NXS_ANOMALY_ASTERISK_CONTEXT=oncall
NXS_ANOMALY_ASTERISK_EXTEN=s
```

Передайте параметры через окружение сервиса. В Helm используйте extraConfig для открытых значений и Secret приложения для ключей. После изменения окружения пересоздайте соответствующие процессы API и воркера.

Для проверки адаптера с правами администратора задайте в оболочке API, API_KEY и USER_ID и выполните команду.

```bash
curl --fail-with-body -X POST "$API/api/v1/users/$USER_ID/test-notification" \
  -H "X-API-Key: $API_KEY" -H 'Content-Type: application/json' \
  -d '{"channel":"call"}'
```

## Контрольная проверка

Перед реальным вызовом проверьте на АТС доступность AMI и настройку dialplan. В примере extension имеет имя s, context — oncall. Если extension отсутствует, вызов может начаться без воспроизведения сообщения.

```bash
asterisk -rx 'manager show settings'
asterisk -rx 'dialplan show s@oncall'
```

## Проверка и устранение неполадок

Отправьте тест на согласованный номер и ответьте на звонок. Успешный Originate подтверждает передачу команды на АТС. При тишине или отсутствии вызова сверьте канал, extension и dialplan в журнале Asterisk. После смены маршрута проверьте каждую резервную АТС.

[Настройка цепочек и доставки](CONFIGURATION.md) · [Каталог интеграций](INTEGRATIONS.md)
