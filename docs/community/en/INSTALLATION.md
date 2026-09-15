# Installation

*Русская версия: [INSTALLATION.md](../ru/INSTALLATION.md)*

Choose **one** method. Docker Compose is the shortest path: it starts PostgreSQL,
the API, worker and web interface together.

| Method | Prerequisites |
|---|---|
| Docker Compose | Docker Engine, Compose 2.20+, Linux/amd64 |
| Kubernetes | Configured kubectl context, Helm 3.8+, a StorageClass |
| Bare metal / VM | Linux with systemd, running PostgreSQL 17, nginx, Go matching `go.mod`, Node.js 22.12+, npm |

All methods use Bash, Git and OpenSSL.

## Prepare configuration

For a **new installation**, choose a published tag from
[Releases](https://github.com/nixys/nxs-anomaly/releases) and run:

```bash
RELEASE_TAG='REPLACE_WITH_RELEASE_TAG'
git clone --branch "$RELEASE_TAG" --depth 1 https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
bash deploy/quickstart/prepare.sh "$RELEASE_TAG"
```

The script generates passwords and ready-to-use files in `.local/`.
Keep this directory: rerunning preparation does not rotate an existing database
password. If you already prepared this checkout, skip this step.
If you plan to use Telegram or email, fill in `.local/delivery.env` before startup.

## Docker Compose

From the checkout root, run:

```bash
docker compose --project-name nxs-anomaly \
  --env-file .local/credentials.env --env-file .local/compose.env \
  -f .local/compose.yaml up -d --wait --wait-timeout 180
```

Open [http://localhost:3100](http://localhost:3100). Sign in as `admin` with the
password from `.local/credentials.env`. If a port is occupied, change it in
`.local/compose.env` and use that port in the URL.

## Kubernetes

Use a new namespace and release named `nxs-anomaly`. Set `STORAGE_CLASS` to a class
available in your cluster (`kubectl get storageclass` lists them), then run:

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

Keep port-forward running. Open [http://localhost:3100](http://localhost:3100)
and sign in as `admin` with the password from `.local/credentials.env`.

## On-premise (bare-metal or virtual machine)

For installation without containers, run the following from the public Community
checkout root. PostgreSQL must already be running and accept password-authenticated
connections on `127.0.0.1:5432`; the commands create a new database and service account.

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

Open [http://localhost:3100](http://localhost:3100) and sign in as `admin` with
the password from `.local/credentials.env`.

For a remote VM or Docker host, open a tunnel **on your workstation**:

```bash
ssh -L 3100:127.0.0.1:3100 user@your-server
```

## Next steps

- [Send your first notification](../../../README.md#get-your-first-notification).
- [Configuration](CONFIGURATION.md) and [troubleshooting](SETUP.md#when-something-does-not-work).
- [Preset files](../../../deploy/quickstart/README.md) and [Helm reference](../../../deploy/helm/nxs-anomaly/README.md).

These instructions use local HTTP for evaluation. Before exposing the service,
configure [HTTPS and access controls](SECURITY_PROFILE.md) and
[backups](BACKUP_RESTORE.md).
