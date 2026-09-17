# Deployment

*Русская версия: [DEPLOY.md](../ru/DEPLOY.md)*

`nxs-anomaly` is a Go binary that talks to PostgreSQL. It can run as one process
with the scheduler built in, or as separate API and worker processes.

A production configuration should use separate components:

- **API**: `nxs-anomaly serve --no-scheduler` (several replicas)
- **Worker**: `nxs-anomaly run-worker --poll-interval 5` (several replicas are
  safe: delivery and retries use an atomic claim, and escalation is sharded by
  integration)

Both use the same database and the same embedded migrations.

The domain entities and their life cycles are described in
[DOMAIN_MODEL.md](DOMAIN_MODEL.md) — useful when setting retention, alerting on
metrics, or building dashboards over the PostgreSQL tables.

## The container image

### Naming release artefacts

One rule for all of them: **`<product>[-component]-<edition>`, with the edition
always last**. So:

| Artefact | Community | Enterprise |
|---|---|---|
| Backend | `nxs-anomaly` | `nxs-anomaly` |
| Frontend | `nxs-anomaly-frontend` | `nxs-anomaly-frontend` |
| Helm chart | `nxs-anomaly` | `nxs-anomaly` |

The community edition is published to GHCR; the enterprise one goes to a private
registry, with an account issued per installation. The tag is the same for all of
them, `vX.Y.Z`, which is also the chart's `appVersion` — so `image.tag` can be
left unset in values.

The frontend image is built separately per edition although its sources are
identical: the interface holds no edition-specific code — it asks the server
through `/api/v1/auth/methods` and `/api/v1/capabilities`. The duplication is
deliberate, so that nobody reading a manifest has to remember that one artefact
in three follows a different rule.

The chart name does **not** reach the names of the objects it creates:
`values.yaml` pins `nameOverride`, so both editions render the same names and the
same Deployment selectors. That is what makes moving between editions an ordinary
`helm upgrade` — a selector cannot be changed, and without the pin the move would
be a reinstall that orphans the volumes.

### Building

```bash
RELEASE_TAG="v$(cat VERSION)"
docker build --build-arg VERSION="$RELEASE_TAG" \
  -t "nxs-anomaly-local:$RELEASE_TAG" .
```

The version is injected into the binary by the `Dockerfile`:

```dockerfile
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
    -ldflags="-s -w -X github.com/nixys/nxs-anomaly/internal/server.Version=${VERSION}" \
    -o /out/nxs-anomaly ./cmd/nxs-anomaly
```

After that `/health` returns it in the `version` field.

The image:

- multi-stage build, `CGO_ENABLED=0`;
- runtime layer `gcr.io/distroless/static-debian12:nonroot`;
- dependencies from `vendor/` (`-mod=vendor`);
- a Docker HEALTHCHECK through `nxs-anomaly healthcheck`;
- port `8080`.

## PostgreSQL

Create the database and the user:

```sql
CREATE USER nxs_anomaly WITH PASSWORD 'change-me';
CREATE DATABASE nxs_anomaly OWNER nxs_anomaly;
```

Set the DSN:

```bash
NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:change-me@postgres:5432/nxs_anomaly?sslmode=disable'
```

Migrations run automatically at startup — see [MIGRATIONS.md](MIGRATIONS.md).

## Kubernetes

### The Helm chart (the recommended route)

The chart is [`deploy/helm/nxs-anomaly`](../../../deploy/helm/nxs-anomaly) (see
its [README](../../../deploy/helm/nxs-anomaly/README.md)). It deploys the API,
the worker and the frontend; the datastores are either bundled (`*.enabled`,
single-node, for development and testing) or external (`external*`).

Secrets come from an existing Secret (`existingSecret`), External Secrets
(`externalSecrets`) or the Vault Secrets Operator (`vaultSecretOperator`); inline
secrets are for development only. There are PodDisruptionBudgets, a
NetworkPolicy, topology spread constraints, a ServiceMonitor and a
PrometheusRule. External traffic can be published through an ordinary Ingress, an
Istio `VirtualService`/`Gateway`, or the Kubernetes Gateway API
`HTTPRoute`/`Gateway`; all three are off by default and independent of each
other.

```bash
# Production: external PostgreSQL and an operator-managed Secret
kubectl create secret generic nxs-anomaly-env \
  --from-literal=NXS_ANOMALY_DB_DSN='postgres://user:pass@pg.prod:5432/nxs_anomaly?sslmode=require'
helm install nxs-anomaly deploy/helm/nxs-anomaly \
  --set existingSecret.enabled=true --set existingSecret.name=nxs-anomaly-env \
  --set inlineSecret.enabled=false --set postgresql.enabled=false \
  --set ingress.enabled=true --set ingress.host=nxs-anomaly.example.com
```

The chart is checked by `helm lint`, `helm unittest -f 'tests/unit/*_test.yaml'`
and a real install/upgrade smoke in kind —
`deploy/helm/nxs-anomaly/tests/e2e/kind-smoke.sh`.

The manual manifests below describe the same thing underneath, for environments
without Helm or for understanding what the chart renders.

The manual YAML below is a structural example, not a complete installation.
Replace `REPLACE_WITH_RELEASE_TAG` with the same published tag for API and worker.
For frontend, login credentials, TLS and complete deployment commands, use
[Installation](INSTALLATION.md).

### Secrets

```bash
kubectl create namespace nxs-anomaly

kubectl -n nxs-anomaly create secret generic nxs-anomaly-config \
  --from-literal=NXS_ANOMALY_DB_DSN='postgres://nxs_anomaly:change-me@postgres.nxs-anomaly.svc.cluster.local:5432/nxs_anomaly?sslmode=disable' \
  --from-literal=NXS_ANOMALY_API_KEY='<a random key>'
```

Provider credentials — optionally in the same Secret or a separate one:

```bash
kubectl -n nxs-anomaly create secret generic nxs-anomaly-providers \
  --from-literal=NXS_ANOMALY_TELEGRAM_BOT_TOKEN='<token>' \
  --from-literal=NXS_ANOMALY_SMTP_HOST='smtp.example.com' \
  --from-literal=NXS_ANOMALY_SMTP_USERNAME='alerts@example.com' \
  --from-literal=NXS_ANOMALY_SMTP_PASSWORD='<password>'
```

### Deployment: API

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nxs-anomaly-api
  namespace: nxs-anomaly
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nxs-anomaly-api
  template:
    metadata:
      labels:
        app: nxs-anomaly-api
    spec:
      containers:
        - name: api
          image: ghcr.io/nixys/nxs-anomaly:REPLACE_WITH_RELEASE_TAG
          args: ["serve", "--host", "0.0.0.0", "--port", "8080", "--no-scheduler"]
          ports:
            - name: http
              containerPort: 8080
          envFrom:
            - secretRef:
                name: nxs-anomaly-config
            - secretRef:
                name: nxs-anomaly-providers
                optional: true
          startupProbe:
            httpGet:
              path: /live     # /live answers from the first second, while the DB and migrations are awaited
              port: http
            periodSeconds: 2
            failureThreshold: 30
          readinessProbe:
            httpGet:
              path: /health   # includes a DB ping; the pod leaves the Service if the DB is down
              port: http
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /live      # cheap, never touches the DB — a DB blip will not restart the pod
              port: http
            periodSeconds: 20
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
```

### Deployment: worker

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nxs-anomaly-worker
  namespace: nxs-anomaly
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nxs-anomaly-worker
  template:
    metadata:
      labels:
        app: nxs-anomaly-worker
    spec:
      containers:
        - name: worker
          image: ghcr.io/nixys/nxs-anomaly:REPLACE_WITH_RELEASE_TAG
          args: ["run-worker", "--poll-interval", "5"]
          ports:
            - name: telemetry
              containerPort: 8081   # /live, /ready, /metrics — NXS_ANOMALY_WORKER_ADDR
          envFrom:
            - secretRef:
                name: nxs-anomaly-config
            - secretRef:
                name: nxs-anomaly-providers
                optional: true
          startupProbe:
            httpGet:
              path: /live
              port: telemetry
            periodSeconds: 2
            failureThreshold: 30
          readinessProbe:
            httpGet:
              path: /ready         # the DB is reachable AND cycles are completing
              port: telemetry
            initialDelaySeconds: 5
            periodSeconds: 10
          livenessProbe:
            httpGet:
              path: /live          # cheap, never touches the DB
              port: telemetry
            initialDelaySeconds: 5
            periodSeconds: 10
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
```

A separate `run-worker` brings up its own telemetry endpoint (`:8081` by default,
see `NXS_ANOMALY_WORKER_ADDR`) serving `/live`, `/ready` and `/metrics`. That
closes an observability blind spot: with `serve --no-scheduler` plus
`run-worker`, the delivery metrics — delivery latency, dead letters, the breaker,
skipped and shift notifications, coverage — were not exported at all, because the
Prometheus registry lived only in the API's HTTP server.

`/ready` reflects not only that the database is reachable but that the worker is
actually **completing cycles**: a hung cycle — a stuck query, a deadlock — takes
the replica out of readiness. The stall threshold is
`NXS_ANOMALY_WORKER_STALL_TIMEOUT_SECONDS`, defaulting to `max(30s, 4×poll)`.

Several worker replicas are safe. Deliveries and retries are picked up by an
atomic claim (`SELECT … FOR UPDATE SKIP LOCKED`, with a unique `worker_id` per
process), escalation is sharded by integration through per-shard advisory locks,
and abandoned claims are reclaimed (`ReclaimStaleClaims`). The absence of double
delivery under contention is confirmed by `TestMultiWorkerNoDoubleDelivery` and
by the `worker_kill`/`retry_storm` profiles of the load harness (see
[CAPACITY.md](CAPACITY.md)).

Contention is not the same as a crash: a replica killed between handing a message
to the provider and saving the result gives its claim back to the reaper, and the
notification goes out a second time. This is at-least-once, that is the guarantee
on offer, and the harness compares duplicates against the number of reclaims
rather than against zero. A single replica only simplifies the order of events;
it is not a correctness requirement.

On `SIGTERM` the worker starts no new cycles, lets the current `RunWorkerCycle`
finish, and shuts its telemetry server down cleanly. The API server stops
accepting new requests through `http.Server.Shutdown` and also waits for the
embedded scheduler if it is enabled.

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nxs-anomaly-api
  namespace: nxs-anomaly
spec:
  selector:
    app: nxs-anomaly-api
  ports:
    - name: http
      port: 8080
      targetPort: http
```

Publish it through an ingress controller. Webhook sources need access to
`/integrations/v1/*`.

## Metrics

The Prometheus scrape endpoint:

```text
GET /metrics
```

Metrics use the `prometheus/client_golang` SDK, with a registry per server
instance:

| Metric | Type | What it is |
|---|---|---|
| `nxs_anomaly_alerts_ingested_total` | counter | alerts accepted |
| `nxs_anomaly_alert_groups_open` | gauge | open groups, read from the database on each scrape |
| `nxs_anomaly_notifications_delivered_total` | counter | notifications delivered |
| `nxs_anomaly_notifications_failed_total` | counter | notifications failed |
| `nxs_anomaly_schedules_degraded` | gauge | schedules with gaps, disabled, or naming people who do not exist, that an escalation chain routes through without `allow_uncovered` |
| `nxs_anomaly_schedules_total` | gauge | schedules |
| `nxs_anomaly_sources_silent` | gauge | integrations with a declared heartbeat that sent nothing in time. A silent source is indistinguishable from a healthy one from the outside |
| `nxs_anomaly_duty_on_call` | gauge | how many people the schedules put on call right now |
| `nxs_anomaly_duty_without_checkin` | gauge | of those, how many have not confirmed the shift. Coverage answers "is somebody assigned"; this answers "did they respond" |
| `nxs_anomaly_shift_notifications_total` | counter | shift-start notifications |
| `nxs_anomaly_notifications_skipped_total` | counter | notifications with no transport (`channel`, `reason`); never counted as delivered |
| `nxs_anomaly_retention_deleted_total` | counter | rows removed by the retention sweep (`category`). A category with a horizon set and a flat counter is a sweep that stopped working. See [DATA_INVENTORY.md](DATA_INVENTORY.md) |
| `nxs_anomaly_notifications_retry_queue_depth` | gauge | notifications in the retry queue |
| `nxs_anomaly_notification_batches_pending` | gauge | open batches |
| `nxs_anomaly_groups_archived_total` | counter | resolved groups archived |
| `nxs_anomaly_worker_cycles_total` | counter | worker cycles completed |
| `nxs_anomaly_worker_cycle_duration_seconds` | gauge | the last cycle's duration |
| `nxs_anomaly_worker_last_cycle_timestamp_seconds` | gauge | Unix time of the last cycle, for stall detection (`time() - metric`); not to be confused with the duration |
| `nxs_anomaly_db_up` | gauge | 1 if the last database ping succeeded |
| `nxs_anomaly_notifications_delivered_by_provider_total` | counter | successful deliveries per channel (`provider`); paired with `_delivery_errors_by_provider_total` it gives a per-provider success-ratio SLI |
| `nxs_anomaly_oldest_due_escalation_age_seconds` | gauge | age of the oldest overdue group |
| `nxs_anomaly_oldest_pending_delivery_age_seconds` | gauge | age of the oldest `delivery_scheduled` |
| `nxs_anomaly_stale_claims_reclaimed_total` | counter | abandoned claims reclaimed (a worker crash, or too short a claim timeout) |

The worker and the API export the same set, the worker on its own port (`:8081`
by default). The delivery metrics are produced by the worker, so with the split
deployment you have to scrape **both** targets.

```yaml
scrape_configs:
  - job_name: nxs-anomaly-api
    static_configs:
      - targets: ['nxs-anomaly-api.nxs-anomaly.svc.cluster.local:8080']
  - job_name: nxs-anomaly-worker
    static_configs:
      - targets: ['nxs-anomaly-worker.nxs-anomaly.svc.cluster.local:8081']
```

The full alert set, the Grafana dashboard, the SLI/SLO definitions and the load
envelope:

- [ALERTING_RULES.md](ALERTING_RULES.md) — the rules and the SLI/SLO with PromQL;
- [prometheus-rules.yaml](../../prometheus-rules.yaml) — a ready `PrometheusRule`;
- [grafana-dashboard.json](../../grafana-dashboard.json) — an importable
  dashboard;
- [CAPACITY.md](CAPACITY.md) — load profiles, failure scenarios, the capacity
  envelope.

## API authentication

People sign in with a password and get an HttpOnly cookie; automation uses
explicitly scoped API keys. The legacy `NXS_ANOMALY_API_KEY` is always admin;
entries in `NXS_ANOMALY_API_KEYS` must carry a role (`token:viewer`,
`token:editor`, …). A key travels in either header:

```text
X-API-Key: <key>
Authorization: Bearer <key>
```

Webhook endpoints authenticate by the integration key in the URL. Additional HMAC
validation of the payload is turned on per integration through its
`webhook_secret` field.

Before a production rollout, choose the retention and egress policy explicitly:

- the four `*_RETENTION_DAYS` horizons — audit, notifications, delivery attempts,
  sessions;
- `NXS_ANOMALY_BLOCKED_CHANNELS` and `NXS_ANOMALY_EGRESS_ALLOWLIST` — the
  permitted channels and destinations.

`GET /api/v1/readiness` lists the decisions still open. The full data map is in
[DATA_INVENTORY.md](DATA_INVENTORY.md), and the safe defaults in
[SECURITY_PROFILE.md](SECURITY_PROFILE.md).

## Upgrade order

1. Build and publish the new image.
2. Read the new migrations in `internal/store/migrations/`.
3. Back the database up.
4. Roll out one API or worker pod — migrations apply exactly once, under advisory
   lock `72544000`.
5. Roll out the remaining pods.
6. Smoke test:

```bash
curl https://nxs-anomaly.example.com/health
curl https://nxs-anomaly.example.com/metrics
```

Starting several instances in parallel is safe: the migration runner is protected
by the advisory lock.
