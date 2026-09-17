# nxs-anomaly Community Helm chart

![nxs-anomaly](https://raw.githubusercontent.com/nixys/nxs-anomaly/main/deploy/helm/nxs-anomaly/logo.png)

**Self-hosted alerting and on-call response for Kubernetes.**

[nxs-anomaly Community](https://github.com/nixys/nxs-anomaly) receives alerts from
Alertmanager, Grafana and webhooks, groups repeated events, routes notifications
to the person on call, and escalates unanswered alerts. This chart deploys the
API, background worker and web interface. PostgreSQL stores application data;
Community does not require a message broker, analytics database or cache.

Community is licensed under **Apache 2.0**. The core alerting workflow is included;
[Enterprise](#community-and-enterprise) adds organization-wide access controls and
analytics. This chart installs Community only.

[Quickstart](#quickstart) · [Production](#production-installation) ·
[Configuration](#configuration-reference) · [Support](#support-and-contributing)

## Prerequisites

- A Kubernetes cluster and `kubectl` access with permission to create the chart's resources.
- Helm 3.8 or later with OCI support and access to GitHub Container Registry.
- PostgreSQL: use the bundled `postgres:17-alpine` instance for evaluation or an
  externally managed database for production.
- For bundled PostgreSQL, a working default StorageClass, or an explicitly set
  `postgresql.persistence.storageClass`.
- An ingress controller and a TLS certificate when publishing the interface.
  Optional integrations need their own operators and CRDs; this chart does not install them.

## Quickstart

For a new installation with bundled PostgreSQL, choose a chart version from
[Releases](https://github.com/nixys/nxs-anomaly/releases) **without** the `v` prefix.
Run in Bash with OpenSSL installed:

```bash
CHART_VERSION='REPLACE_WITH_CHART_VERSION'
ADMIN_PASSWORD=$(openssl rand -hex 24)
helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$CHART_VERSION" --namespace nxs-anomaly --create-namespace \
  --set postgresql.enabled=true --set inlineSecret.enabled=true \
  --set-string postgresql.auth.password="$(openssl rand -hex 24)" \
  --set-string inlineSecret.data.NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin \
  --set-string inlineSecret.data.NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  --set-string config.NXS_ANOMALY_SESSION_COOKIE_SECURE=false \
  --wait --timeout 5m
printf 'Admin password: %s\n' "$ADMIN_PASSWORD"
kubectl -n nxs-anomaly port-forward service/nxs-anomaly-frontend 3100:8080
```

Open [http://localhost:3100](http://localhost:3100) and sign in as `admin` with
the printed password. Keep port-forward running.

If the cluster has no default StorageClass, add
`--set-string postgresql.persistence.storageClass=YOUR_STORAGE_CLASS` to the
Helm command. Save the administrator password; generated credentials also reside
in Helm release values. This is a first-install example: reuse existing passwords
when upgrading, since new values do not rotate the password in PostgreSQL.

Continue with [your first notification](https://github.com/nixys/nxs-anomaly#get-your-first-notification).
Use the production instructions below for external access through HTTPS.

## Production installation

Have an external PostgreSQL database with backups, an ingress controller, and
DNS for your service ready. In namespace `nxs-anomaly`, provision the TLS Secret
`nxs-anomaly-tls` for your hostname. Use your own ingress class and hostname below.

Create a private `credentials.env` file:

```dotenv
NXS_ANOMALY_DB_DSN=postgres://nxs_anomaly:REPLACE_WITH_PASSWORD@pg.example.com:5432/nxs_anomaly?sslmode=require
NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin
NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD=REPLACE_WITH_STRONG_PASSWORD
```

URL-encode special characters in DSN credentials; env values need no shell quotes.
Add the [Telegram, SMTP or other provider credentials](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/CONFIGURATION.md)
you need to the same file. API and worker both read this Secret.

Save `production-values.yaml`:

```yaml
existingSecret:
  enabled: true
  name: nxs-anomaly-env
config:
  NXS_ANOMALY_PROFILE: "production"
  NXS_ANOMALY_TEAM_SCOPING: "false" # Community uses one shared access domain.
ingress:
  enabled: true
  className: nginx
  host: alerts.example.com
  tls:
    - secretName: nxs-anomaly-tls
      hosts: [alerts.example.com]
```

Choose a published chart version (without `v`) and install:

```bash
CHART_VERSION='REPLACE_WITH_CHART_VERSION'
chmod 600 credentials.env
kubectl -n nxs-anomaly create secret generic nxs-anomaly-env \
  --from-env-file=credentials.env
helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$CHART_VERSION" --namespace nxs-anomaly \
  --values production-values.yaml --wait --timeout 5m
```

Open your HTTPS hostname and sign in with the administrator credentials from
`credentials.env`. Then [configure your first notification](https://github.com/nixys/nxs-anomaly#get-your-first-notification).

The profile enables secure cookies and the outbound webhook SSRF guard; the chart
does not provision your database, backups, certificates or notification providers.
The sample DSN encrypts traffic; use `verify-full` with a trusted CA to also verify
the database server's identity. Further settings: [security](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/SECURITY_PROFILE.md)
and [capacity](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/CAPACITY.md).

## Configuration reference

These are the chart defaults, before either example above is applied. Inspect
all values for your selected release with:

```bash
helm show values oci://ghcr.io/nixys/nxs-anomaly --version "$CHART_VERSION" > chart-defaults.yaml
```

| Value | Default | Purpose |
|---|---|---|
| `image.tag`, `frontend.image.tag` | `""` | Use the chart's `appVersion` |
| `api.replicaCount` | `2` | API replicas |
| `worker.replicaCount` | `1` | Background worker replicas |
| `frontend.enabled`, `frontend.replicaCount` | `true`, `2` | Web interface and same-origin API proxy |
| `existingSecret.enabled`, `existingSecret.name` | `false`, `""` | Use a Secret in the release namespace |
| `externalSecrets.enabled` | `false` | Create an External Secrets Operator resource |
| `vaultSecretOperator.enabled` | `false` | Create a Vault Secrets Operator resource |
| `inlineSecret.enabled` | `false` | Put credentials in Helm values; evaluation only |
| `postgresql.enabled` | `false` | Deploy a single-node PostgreSQL StatefulSet |
| `postgresql.auth.password` | `change-me` | Replace before enabling bundled PostgreSQL |
| `postgresql.persistence.enabled`, `.size` | `true`, `5Gi` | Bundled database storage |
| `externalPostgres.sslmode` | `require` | TLS mode when the chart assembles a DSN |
| `config.NXS_ANOMALY_PROFILE` | `""` | Set to `production` for the security profile |
| `config.NXS_ANOMALY_SESSION_COOKIE_SECURE` | `"true"` | Require HTTPS for session cookies |
| `config.NXS_ANOMALY_TRUSTED_PROXIES` | private ranges | Proxies whose `X-Forwarded-For` is believed; narrow to your pod and ingress CIDRs if clients also connect from private networks |
| `ingress.enabled` | `false` | Publish the frontend through an Ingress |
| `networkPolicy.enabled` | `false` | Enable chart NetworkPolicies; requires an enforcing CNI |
| `serviceMonitor.enabled`, `prometheusRule.enabled` | `false`, `false` | Prometheus Operator integration |
| `tracing.enabled` | `false` | OTLP/HTTP tracing; also set `tracing.endpoint` |
| `tests.acceptance.enabled` | `false` | Opt-in test that writes a canary alert |

Exactly one secret source must be enabled. Its resulting Secret must contain
`NXS_ANOMALY_DB_DSN`; add bootstrap credentials for a fresh installation and
provider credentials as needed. Inline mode can assemble a DSN from
`postgresql.auth` or `externalPostgres` values. With an existing Secret, its DSN
is authoritative; `externalPostgres.*` does not override it.

External Secrets Operator and Vault Secrets Operator must already be installed.
For Vault, configure either an existing `vaultSecretOperator.vaultAuthRef` or
`vaultSecretOperator.vaultAuth.create`; the chart does not create a
`VaultConnection`.

Use at most one ingress option: `ingress.enabled`,
`istio.virtualService.enabled` or `gatewayAPI.httpRoute.enabled`. Istio and
Gateway API require their respective controllers and CRDs. Route traffic through
the frontend for the web interface and same-origin API access.

With NetworkPolicy enabled, configure ingress for your ingress controller and
Prometheus through `networkPolicy.extraIngress` as needed. Review the rendered
policies against your cluster; API/worker outbound access is not restricted to a
list of notification providers. `rateLimits.webhookRatePerCluster` and
`rateLimits.apiRatePerCluster` divide a rate across API replicas with independent
per-pod buckets; they are not a coordinated global quota.

Helm validates types with `values.schema.json` and checks incompatible settings
before rendering. The [deployment guide](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/DEPLOY.md#kubernetes)
provides additional context; use the files from your release tag when their
contents differ from `main`.

## Verify release artifacts

The Community release workflow uses keyless cosign signatures for the chart and
container images. The chart additionally has GPG-signed Helm provenance (`.prov`).
These mechanisms verify artifact integrity and signer identity, not freedom from
vulnerabilities. Verify the selected release before installation.

For cosign, check the exact release workflow identity. The chart tag has no `v`:

```bash
cosign verify \
  --certificate-identity "https://github.com/nixys/nxs-anomaly/.github/workflows/release.yml@refs/tags/v${CHART_VERSION}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "ghcr.io/nixys/nxs-anomaly:${CHART_VERSION}"
```

For Helm provenance, download the published public key, check its fingerprint
against the maintainer's published signing-key information, and create a binary
GPG keyring explicitly. This also works when your normal GPG keyring uses the
newer keybox format.

```bash
VERIFY_DIR=$(mktemp -d)
curl --fail --silent --show-error --location \
  https://raw.githubusercontent.com/nixys/nxs-anomaly/main/assets/nxs-anomaly-helm-signing.pub.asc \
  --output "$VERIFY_DIR/signing-key.asc"
gpg --show-keys --with-fingerprint "$VERIFY_DIR/signing-key.asc"
# Check the fingerprint before continuing.
gpg --batch --dearmor --output "$VERIFY_DIR/keyring.gpg" "$VERIFY_DIR/signing-key.asc"
helm pull oci://ghcr.io/nixys/nxs-anomaly --version "$CHART_VERSION" \
  --verify --keyring "$VERIFY_DIR/keyring.gpg" --destination "$VERIFY_DIR"
```

See the [security policy](https://github.com/nixys/nxs-anomaly/blob/main/SECURITY.md#supply-chain)
for image verification. The signing-key fingerprint is published in the
[chart metadata](https://github.com/nixys/nxs-anomaly/blob/main/deploy/helm/nxs-anomaly/Chart.yaml)
under `artifacthub.io/signKey`; check the metadata for your release.

## Upgrading and uninstalling

The project is **pre-1.0**. Read [release notes](https://github.com/nixys/nxs-anomaly/releases)
and [migrations](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/MIGRATIONS.md)
for your target version. Back up PostgreSQL before upgrading. Migrations run at
startup under an advisory lock and are forward-only; `helm rollback` does not
reverse them. Prepare a [database restore plan](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/BACKUP_RESTORE.md).

Set `CHART_VERSION` to the target version and review your saved values against
that release's defaults. For the production example:

```bash
helm upgrade nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$CHART_VERSION" --namespace nxs-anomaly \
  --values production-values.yaml --wait --timeout 5m
helm test nxs-anomaly --namespace nxs-anomaly
```

Use `evaluation-values.yaml` instead for the evaluation installation. To uninstall:

```bash
helm uninstall nxs-anomaly --namespace nxs-anomaly
```

Externally managed PostgreSQL is unaffected. The bundled StatefulSet's database
PVC is retained by the current chart; check your storage/reclaim policy and
remove retained data explicitly only when you no longer need it.

## Testing and troubleshooting

`helm test` runs the chart's connection test. The optional acceptance test
(`tests.acceptance.enabled=true`) needs an admin API key in the application Secret,
under `NXS_ANOMALY_ACCEPTANCE_API_KEY` by default. It creates a canary integration,
checks ingest, a delivery attempt, acknowledgement, resolution and audit history,
then cleans up. A delivery attempt does not prove that an external recipient
received a message; test your real notification channel separately.

For pending pods, check scheduling and PVC events with `kubectl describe`. For
startup failures, check the database DSN, Secret keys and API/worker logs. For
missing notifications, inspect the alert's delivery attempts and worker logs.
See [Setup troubleshooting](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/SETUP.md#when-something-does-not-work).

Contributors can validate a local checkout with Helm and the `helm-unittest` plugin:

```bash
helm lint deploy/helm/nxs-anomaly --set inlineSecret.enabled=true --set postgresql.enabled=true
helm unittest -f 'tests/unit/*_test.yaml' deploy/helm/nxs-anomaly
```

## Community and Enterprise

| Need | Community | Enterprise |
|---|---|---|
| Ingest, grouping, on-call schedules, escalation and delivery | Included | Included |
| Web UI, ChatOps, audit history, API and operational metrics | Included | Included |
| Password authentication and role-based permissions | Included | Included |
| Corporate OIDC sign-in and team access boundaries | — | Included; configuration required |
| Lifecycle analytics and on-call quality reports | — | Requires a separately configured analytics pipeline and database |
| Support | Community issues, best effort | Scope and response terms agreed with Nixys |

Community teams organize routing; team membership does not isolate access to
objects. Consider Enterprise when multiple teams need separate visibility,
corporate authentication, or reporting on overdue acknowledgements, night-time
load and delivery problems.

**[Discuss an Enterprise evaluation with Nixys](https://nixys.io/contacts/).**
Share your Community version, deployment method, number of teams and requirements.
Enterprise uses separate artifacts: changing Community chart values does not
unlock it. Plan migration with compatible versions and a database backup.

## Support and contributing

Maintained by [Nixys](https://nixys.io/). For bugs or Community questions,
[open an issue](https://github.com/nixys/nxs-anomaly/issues) with the chart/application
versions, Kubernetes version, reproduction steps and sanitized values/logs.
See [support scope](https://github.com/nixys/nxs-anomaly/blob/main/SUPPORT.md).
Report vulnerabilities privately through the
[security policy](https://github.com/nixys/nxs-anomaly/blob/main/SECURITY.md).

Documentation improvements and code contributions are welcome; read
[CONTRIBUTING.md](https://github.com/nixys/nxs-anomaly/blob/main/CONTRIBUTING.md).
Documentation is available in
[English](https://github.com/nixys/nxs-anomaly/tree/main/docs/community/en) and
[Russian](https://github.com/nixys/nxs-anomaly/tree/main/docs/community/ru).

## License

Community is distributed under the [Apache License 2.0](https://github.com/nixys/nxs-anomaly/blob/main/LICENSE).
Enterprise is licensed separately.
