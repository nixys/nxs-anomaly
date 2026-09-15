# Установка

*In English: [INSTALLATION.md](../en/INSTALLATION.md)*

Выберите **один** способ. Самый короткий путь — Docker Compose: он одновременно
запускает PostgreSQL, API, фоновый обработчик и веб-интерфейс.

| Способ | Что должно быть установлено |
|---|---|
| Docker Compose | Docker Engine, Compose 2.20+, Linux/amd64 |
| Kubernetes | Настроенный контекст kubectl, Helm 3.8+, StorageClass |
| Bare metal / VM | Linux с systemd, работающий PostgreSQL 17, nginx, Go согласно `go.mod`, Node.js 22.12+, npm |

Для всех способов нужны Bash, Git и OpenSSL.

## Подготовка конфигурации

Для **новой установки** выберите опубликованный тег в
[Releases](https://github.com/nixys/nxs-anomaly/releases) и выполните:

```bash
RELEASE_TAG='REPLACE_WITH_RELEASE_TAG'
git clone --branch "$RELEASE_TAG" --depth 1 https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
bash deploy/quickstart/prepare.sh "$RELEASE_TAG"
```

Скрипт создаст пароли и готовые конфиги в `.local/`.
Сохраните этот каталог: повторная подготовка не меняет пароль существующей БД.
Если этот каталог уже подготовлен, пропустите шаг.
Для Telegram или почты заполните `.local/delivery.env` перед запуском.

## Docker Compose

Из корня репозитория выполните:

```bash
docker compose --project-name nxs-anomaly \
  --env-file .local/credentials.env --env-file .local/compose.env \
  -f .local/compose.yaml up -d --wait --wait-timeout 180
```

Откройте [http://localhost:3100](http://localhost:3100). Войдите как `admin`
с паролем из `.local/credentials.env`. Если порт занят, измените его в
`.local/compose.env` и используйте новый порт в адресе.

## Kubernetes

Используйте новое пространство имён и релиз `nxs-anomaly`. Укажите в `STORAGE_CLASS`
существующий класс хранения (список: `kubectl get storageclass`) и выполните:

```bash
STORAGE_CLASS='REPLACE_WITH_STORAGE_CLASS'
. .local/credentials.env

kubectl create namespace nxs-anomaly
kubectl -n nxs-anomaly create secret generic nxs-anomaly-env \
  --from-env-file=.local/credentials.env \
  --from-env-file=.local/delivery.env \
  --from-env-file=.local/kubernetes.env

helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$(cat VERSION)" --namespace nxs-anomaly \
  --values deploy/quickstart/kubernetes-values.yaml \
  --set-string postgresql.auth.password="$POSTGRES_PASSWORD" \
  --set-string postgresql.persistence.storageClass="$STORAGE_CLASS" \
  --wait --timeout 5m
kubectl -n nxs-anomaly port-forward service/nxs-anomaly-frontend 3100:8080
```

Оставьте port-forward работающим. Откройте [http://localhost:3100](http://localhost:3100)
и войдите как `admin` с паролем из `.local/credentials.env`.

## Bare metal или VM без контейнеров

Выполните команды из корня публичного Community-репозитория. PostgreSQL должен
уже работать и принимать подключения по паролю на `127.0.0.1:5432`.
Команды создают новую базу данных и системного пользователя сервиса.

```bash
go build -mod=vendor -trimpath \
  -ldflags="-s -w -X github.com/nixys/nxs-anomaly/internal/server.Version=v$(cat VERSION)" \
  -o .local/nxs-anomaly ./cmd/nxs-anomaly
(cd frontend && npm ci && npm run build)

sudo useradd --system --create-home --home-dir /var/lib/nxs-anomaly \
  --shell /usr/sbin/nologin nxs-anomaly
sudo install -m 0755 .local/nxs-anomaly /usr/local/bin/nxs-anomaly
sudo install -d -m 0755 /var/www/nxs-anomaly
sudo cp -a frontend/dist/. /var/www/nxs-anomaly/
sudo chmod -R a+rX /var/www/nxs-anomaly

. .local/credentials.env

sudo -u postgres psql -v ON_ERROR_STOP=1 -v db_password="$POSTGRES_PASSWORD" <<'SQL'
CREATE ROLE nxs_anomaly WITH LOGIN PASSWORD :'db_password';
CREATE DATABASE nxs_anomaly OWNER nxs_anomaly;
SQL

sudo install -d -m 0700 /etc/nxs-anomaly
sudo install -m 0600 .local/credentials.env .local/delivery.env \
  .local/on-premise.env /etc/nxs-anomaly/

sudo install -m 0644 deploy/quickstart/nxs-anomaly-*.service /etc/systemd/system/

sudo systemctl daemon-reload
sudo systemctl enable --now nxs-anomaly-api nxs-anomaly-worker

sudo install -m 0644 deploy/quickstart/nginx.conf /etc/nginx/conf.d/nxs-anomaly.conf
sudo nginx -t
sudo systemctl enable --now nginx
sudo systemctl reload nginx
```

Откройте [http://localhost:3100](http://localhost:3100) и войдите как `admin`
с паролем из `.local/credentials.env`.

Для удалённой VM или Docker-хоста откройте туннель **на рабочем компьютере**:

```bash
ssh -L 3100:127.0.0.1:3100 user@your-server
```

## После запуска

- [Отправьте первое уведомление](../../../README.md#get-your-first-notification).
- [Настройка](CONFIGURATION.md) и [диагностика](SETUP.md#диагностика).
- [Готовые конфиги](../../../deploy/quickstart/README.md) и [справочник Helm](../../../deploy/helm/nxs-anomaly/README.md).

Эти инструкции используют локальный HTTP для знакомства с продуктом. Перед
открытием внешнего доступа настройте [HTTPS и права доступа](SECURITY_PROFILE.md),
а также [резервное копирование](BACKUP_RESTORE.md).
