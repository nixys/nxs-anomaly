# External PostgreSQL

## Before you begin

Provision a database and login role owned by the application, with privileges to create and migrate its schema. Allow API and worker connections and provide a trusted certificate for the database hostname. [Installation options](INSTALLATION.md). [Install PostgreSQL](https://www.postgresql.org/download/).

## Connect and configure

1. Create a Kubernetes Secret with `NXS_ANOMALY_DB_DSN` containing the PostgreSQL connection URI and credentials.
2. Select that secret and disable the bundled database with the values below.
3. Back up an existing database before upgrading the service.

```yaml
postgresql:
  enabled: false
existingSecret:
  enabled: true
  name: nxs-anomaly-env
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

Use a test connection configured with the same host, TLS settings and role as the service. When changing databases, verify the target database and user before routing the API and worker to it.

```sql
SELECT current_database(), current_user;
SELECT version FROM nxs_anomaly_schema_migrations ORDER BY version DESC LIMIT 1;
```

## Verify and troubleshoot

Read /health and verify `db_ok`, then create a test group and confirm the worker advances its chain. For connection errors inspect DNS, pg_hba.conf, TLS trust and the Secret used by both pods. Periodically test restoring backups into an isolated database.

[Back up and restore PostgreSQL](BACKUP_RESTORE.md) · [Integration catalogue](INTEGRATIONS.md)
