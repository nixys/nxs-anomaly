# Внешний PostgreSQL

## Перед началом

Подготовьте БД и роль приложения с правами создавать и обновлять схему. Разрешите соединения от API и воркера и подготовьте доверенный сертификат для имени хоста БД. [Способы установки](INSTALLATION.md). [Установка PostgreSQL](https://www.postgresql.org/download/).

## Подключение и настройка

1. Создайте в Kubernetes Secret с `NXS_ANOMALY_DB_DSN`, содержащим URI PostgreSQL и учётные данные.
2. Укажите этот секрет и отключите встроенную БД параметрами ниже.
3. Перед обновлением сервиса сделайте резервную копию существующей БД.

```yaml
postgresql:
  enabled: false
existingSecret:
  enabled: true
  name: nxs-anomaly-env
```

Добавьте фрагмент в values релиза с действующими настройками БД, секретов и образов. В репозитории версии установленного чарта задайте RELEASE и NS и примените полный файл values.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Контрольная проверка

Подключитесь к тестовой БД с тем же хостом, ролью и настройками TLS, что у приложения. Перед переключением API и воркера сверьте целевую БД и пользователя.

```sql
SELECT current_database(), current_user;
SELECT version FROM nxs_anomaly_schema_migrations ORDER BY version DESC LIMIT 1;
```

## Проверка и устранение неполадок

Проверьте `db_ok` в /health, создайте тестовую группу и убедитесь, что воркер выполняет цепочку. При ошибках проверьте DNS, pg_hba.conf, CA и Secret обоих подов. Периодически проверяйте восстановление копии в отдельную БД.

[Резервное копирование и восстановление PostgreSQL](BACKUP_RESTORE.md) · [Каталог интеграций](INTEGRATIONS.md)
