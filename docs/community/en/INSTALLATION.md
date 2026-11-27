# Quick start

Start Community with Helm in Kubernetes or Docker Compose on a Linux/amd64 host. Both examples start PostgreSQL, the API, worker and web interface.

## Prepare a release

With Bash, Git and OpenSSL installed, choose a tag from [Releases](https://github.com/nixys/nxs-anomaly/releases) and substitute it below. Run these commands once for either method.

```bash
RELEASE_TAG='REPLACE_WITH_RELEASE_TAG'
git clone --branch "$RELEASE_TAG" --depth 1 https://github.com/nixys/nxs-anomaly.git
cd nxs-anomaly
bash deploy/quickstart/prepare.sh "$RELEASE_TAG"
```

Keep the generated `.local/` directory for subsequent starts. Choose one method below.

## Kubernetes

Use Helm 3.8+ and kubectl configured for the target cluster. Run `kubectl get storageclass` and replace the storage class placeholder with an available class. Use a fresh namespace for this example.

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

Keep port forwarding running while using the UI.

## Docker Compose

With Docker Engine and Compose 2.20+ installed, run from the checkout root.

```bash
docker compose --project-name nxs-anomaly \
  --env-file .local/credentials.env --env-file .local/compose.env \
  -f .local/compose.yaml up -d --wait --wait-timeout 180
```

## Verify the installation

In another terminal, check API and database readiness.

```bash
curl --fail-with-body http://localhost:3100/health
```

Expect HTTP 200 and `"db_ok": true`. Open [the web interface](http://localhost:3100) and sign in as `admin` with `NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD` from `.local/credentials.env`. Send a [test alert](GUIDE_WEBHOOK.md) to check routing and delivery.

[Bare metal or VM](DEPLOY.md#on-premise-bare-metal-or-virtual-machine) · [Production security](SECURITY_PROFILE.md).
