# OpenCVE → nxs-anomaly

The adapter receives OpenCVE webhook batches and forwards normalized alerts to
the nxs-anomaly generic webhook. See the installation, Telegram routing and
troubleshooting guide. [English](../../docs/community/en/OPENCVE.md) ·
[Русский](../../docs/community/ru/OPENCVE.md)

| Variable | Purpose |
| --- | --- |
| `OPENCVE_WEBHOOK_TOKEN` | Required shared token for incoming `X-OpenCVE-Token` headers |
| `NXS_ANOMALY_WEBHOOK_URL` | Required generic integration ingest URL |
| `NXS_ANOMALY_WEBHOOK_SECRET` | Optional HMAC secret for signed requests to nxs-anomaly |
| `OPENCVE_ROUTING` | `project` (default) or `subscription` |
| `OPENCVE_DB` | SQLite path. Defaults to `/data/delivered.sqlite3` |

Build from the repository root with `docker build -t nxs-opencve-adapter deploy/opencve`.
The container listens on port 8080. Use `GET /health` to check the HTTP process
and mount a persistent volume at `/data` to retain deduplication across restarts.
