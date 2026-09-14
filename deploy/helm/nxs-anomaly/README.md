# nxs-anomaly Community Helm chart

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

This example creates a local evaluation with persistent PostgreSQL and an initial
administrator. It uses HTTP through a loopback port-forward. You also need Bash
and OpenSSL for the password generation commands.

Choose a published version from [Releases](https://github.com/nixys/nxs-anomaly/releases).
Set `CHART_VERSION` below before running the commands: chart versions use `X.Y.Z`,
without the `v` prefix. Application images use `vX.Y.Z`; leave image tags unset to
use the matching chart `appVersion`.

```bash
CHART_VERSION='REPLACE_WITH_CHART_VERSION'
umask 077
cat > evaluation-values.yaml <<EOF_VALUES
postgresql:
  enabled: true
  auth:
    password: "$(openssl rand -hex 24)"
inlineSecret:
  enabled: true
  data:
    NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME: "admin"
    NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD: "$(openssl rand -hex 24)"
config:
  NXS_ANOMALY_SESSION_COOKIE_SECURE: "false"
EOF_VALUES

helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$CHART_VERSION" --namespace nxs-anomaly --create-namespace \
  --values evaluation-values.yaml --wait --timeout 5m

kubectl --namespace nxs-anomaly port-forward service/nxs-anomaly-frontend 3100:8080
```

Open [http://localhost:3100](http://localhost:3100). Sign in as `admin` using the
administrator password saved in `evaluation-values.yaml`. Keep this file private
and reuse it for this installation; regenerating it does not rotate an existing
database password. Inline secrets are also stored in Helm release data.

In **Setup**, create people and notification targets, a team and current on-call
rotation, an escalation chain and an integration. Configure channel credentials
for the worker, send a test alert and confirm that a notification reaches its
recipient and can be acknowledged. The default `log` target only writes to logs.
See the [first-alert walkthrough](https://github.com/nixys/nxs-anomaly#get-your-first-notification).

HTTP cookies are enabled only for this local example. Use HTTPS and keep
`NXS_ANOMALY_SESSION_COOKIE_SECURE: "true"` for a published installation.

## Production installation

Provision PostgreSQL, database backups, DNS, an ingress controller and a TLS
Secret first. The example below assumes an ingress class named `nginx` and a TLS
Secret named `nxs-anomaly-tls` in the application namespace. Replace the example
hostnames and credentials with your own.

Create a private `credentials.env` file containing these keys. URL-encode special
characters in the PostgreSQL username and password; do not put shell quotes
around values in this file.

```dotenv
NXS_ANOMALY_DB_DSN=postgres://nxs_anomaly:REPLACE_WITH_PASSWORD@pg.example.com:5432/nxs_anomaly?sslmode=require
NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME=admin
NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD=REPLACE_WITH_STRONG_PASSWORD
```

Add the notification provider credentials your worker needs; see
[Configuration](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/CONFIGURATION.md).
The sample DSN requires encrypted transport. For database server identity
verification, configure `verify-full` with the appropriate trusted CA.

```bash
chmod 600 credentials.env
kubectl create namespace nxs-anomaly --dry-run=client -o yaml | kubectl apply -f -
kubectl --namespace nxs-anomaly create secret generic nxs-anomaly-env \
  --from-env-file=credentials.env
```

Save the following as `production-values.yaml`:

```yaml
existingSecret:
  enabled: true
  name: nxs-anomaly-env
config:
  NXS_ANOMALY_PROFILE: "production"
  # Community is one shared access domain; team isolation requires Enterprise.
  NXS_ANOMALY_TEAM_SCOPING: "false"
ingress:
  enabled: true
  className: nginx
  host: alerts.example.com
  tls:
    - secretName: nxs-anomaly-tls
      hosts:
        - alerts.example.com
```

```bash
helm install nxs-anomaly oci://ghcr.io/nixys/nxs-anomaly \
  --version "$CHART_VERSION" --namespace nxs-anomaly \
  --values production-values.yaml --wait --timeout 5m
```

Open your configured HTTPS hostname and complete Setup. The production profile
requires external datastores and managed secrets, retains secure session cookies
and the outbound webhook SSRF guard, and checks replica/PDB settings. It does not
provision database backups, certificates or notification providers. Review the
[security profile](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/SECURITY_PROFILE.md)
and [capacity guide](https://github.com/nixys/nxs-anomaly/blob/main/docs/community/en/CAPACITY.md)
before using the service for on-call response.

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
