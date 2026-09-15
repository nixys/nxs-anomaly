# Community installation presets

These files belong to the same release as the checkout. Run commands from the
repository root. Start with the [installation guide](../../docs/community/en/INSTALLATION.md)
([на русском](../../docs/community/ru/INSTALLATION.md)).

| File | Purpose |
|---|---|
| `prepare.sh` | Generate private `.local/` configuration once; refuse to overwrite credentials |
| `delivery.env.example` | Optional Telegram and SMTP transport settings |
| `compose.env.example` / `compose.yaml` | Matching images, loopback ports, API, worker, frontend and PostgreSQL |
| `nginx.conf` | Ready-to-use native frontend config for localhost:3100 and API localhost:8080 |
| `on-premise.env.example` | Local PostgreSQL connection and native process settings |
| `nxs-anomaly-api.service` / `nxs-anomaly-worker.service` | Separate systemd API and worker services |
| `kubernetes.env.example` | DSN for the `nxs-anomaly` Helm release |
| `kubernetes-values.yaml` | Evaluation PostgreSQL, persistence and existing application Secret |
| `main.tf` | Minimal Terraform user resource for an already running API |

`prepare.sh` fills the variables in `.example` files. The nginx config is ready
to copy; Kubernetes values take the database password and StorageClass through
the Helm parameters shown in the installation guide.
Do not pass an unrendered DSN to a service. The helper generates hexadecimal
credentials to avoid incompatible env-file quoting rules. Set a real transport
in `.local/delivery.env`, then recreate/restart the API and worker. Configuration
is read at process startup. Never commit `.local/` or Terraform state.

PostgreSQL passwords must stay consistent with an existing data volume. These
presets are for evaluation; production needs HTTPS, backups and an external
managed database where appropriate. No SMTP server or bot is provisioned here.
