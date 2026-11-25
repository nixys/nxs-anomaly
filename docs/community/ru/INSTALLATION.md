# Быстрый старт

Запустите Community через Helm в Kubernetes или через Docker Compose на Linux/amd64. Оба варианта запускают PostgreSQL, API, воркер и веб-интерфейс.

## Подготовьте релиз

Понадобятся Bash, Git и OpenSSL. Подставьте тег из [списка релизов](https://github.com/nixys/nxs-anomaly/releases). Выполните команды один раз для выбранного способа запуска.

```bash
RELEASE_TAG='REPLACE_WITH_RELEASE_TAG'
git clone --branch "$RELEASE_TAG" --depth 1 https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
bash deploy/quickstart/prepare.sh "$RELEASE_TAG"
```

Сохраните каталог `.local/` для повторных запусков. Выберите один из двух вариантов ниже.

## Kubernetes

Нужны Helm 3.8+ и kubectl с контекстом нужного кластера. Выполните `kubectl get storageclass` и подставьте имя доступного класса хранения. Пример рассчитан на новый namespace.

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

Оставьте port-forward запущенным на время работы с интерфейсом.

## Docker Compose

Установите Docker Engine и Compose 2.20+. Выполните из корня репозитория.

```bash
docker compose --project-name nxs-anomaly \
  --env-file .local/credentials.env --env-file .local/compose.env \
  -f .local/compose.yaml up -d --wait --wait-timeout 180
```

## Проверьте установку

В другом терминале проверьте доступность API и базы данных.

```bash
curl --fail-with-body http://localhost:3100/health
```

Ожидайте HTTP 200 и `"db_ok": true`. Откройте [веб-интерфейс](http://localhost:3100) и войдите как `admin` с паролем `NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD` из `.local/credentials.env`. Отправьте [тестовый алерт](GUIDE_WEBHOOK.md), чтобы проверить маршрутизацию и доставку.

[Установка на виртуальную машину](DEPLOY.md#on-premise-физический-сервер-или-виртуальная-машина) · [Защита рабочей установки](SECURITY_PROFILE.md).
