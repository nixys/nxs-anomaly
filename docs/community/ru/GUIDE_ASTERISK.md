# Звонки через Asterisk AMI

## Встроенный Asterisk в Kubernetes

Включите шлюз с одной репликой для исходящих звонков через SIP-транк по UDP. Создайте в пространстве имён релиза Secret с ключами `ami-password` (случайный пароль AMI) и `sip-password` (пароль оператора). Пароли однострочные, допустимы латинские буквы, цифры и символы из `_./=@%:+-`. Укажите в Helm адрес и логин транка, разрешённый исходящий номер и сети сигнализации и медиашлюзов оператора.

```yaml
asterisk:
  enabled: true
  existingSecret: oncall-voice
  trunkHost: sip.example.org
  trunkPort: 5060
  username: oncall
  callerID: "+15550100000"
  providerCIDRs: ["192.0.2.0/24"]
```

При использовании внутреннего зеркала скопируйте все платформы образа с сохранением digest и задайте `asterisk.image: mirror.example.org/voice/asterisk@sha256:<digest из values.yaml>`. Если соединение с AMI потеряно после отправки Originate, проверьте историю вызовов АТС перед ручным повтором. Попытка получает код `asterisk_originate_unconfirmed`, резервная АТС и автоматические повторы для этой попытки отключены.

Регистрация включена по умолчанию. Если транку не нужен REGISTER, задайте `asterisk.register: false`. Оператору нужен маршрут до объявленного адреса пода на UDP 5060 (SIP) и 10000–10019 (RTP). При NAT с сохранением портов настройте переадресацию к поду и задайте оба параметра `asterisk.externalAddress` и `asterisk.localNet`. Чарт создаёт внутренний Service только для AMI. При включённой NetworkPolicy перечислите все сети оператора в `providerCIDRs`. Чарт передаёт настройки AMI в API и воркер. Уберите прежние нумерованные экземпляры Asterisk и параметры прокси для звонков, если они перенаправляют соединение мимо шлюза. После ротации паролей перезапустите шлюз, API и воркер.

Укажите телефон пользователя цифрами с необязательным ведущим `+`, включите канал `call` и отправьте тестовое уведомление по примеру ниже. Ответьте и прослушайте запись «Monitoring alert. Please check your incident notifications». Затем вызовите тестовую эскалацию через воркер. Успех AMI означает, что АТС поставила звонок в очередь. Для существующей внешней АТС используйте инструкцию ниже.


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
