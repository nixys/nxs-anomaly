# External Secrets Operator

## Before you begin

Install an operator serving external-secrets.io/v1beta1 and configure a Ready SecretStore or ClusterSecretStore with read access to the application secret. Store the PostgreSQL DSN and provider credentials there. [Installation options](INSTALLATION.md). [Install External Secrets Operator](https://external-secrets.io/latest/introduction/getting-started/).

## Connect and configure

1. Select externalSecrets as the chart's sole primary secret source.
2. Replace the store, key and property in this example.
3. Add one mapping per required variable.
4. Apply the release and wait for synchronization before starting traffic.

```yaml
externalSecrets:
  enabled: true
  secretStoreRef: {name: app-store, kind: ClusterSecretStore}
  data:
    - secretKey: NXS_ANOMALY_DB_DSN
      remoteRef: {key: nxs-anomaly, property: db_dsn}
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

Use the release namespace and the generated resource name. Verify the DSN key exists without printing its value.

```bash
kubectl -n "$NS" get externalsecret
kubectl -n "$NS" describe externalsecret "$NAME"
kubectl -n "$NS" get secret "$NAME" -o json | jq -e '.data | has("NXS_ANOMALY_DB_DSN")'
```

## Verify and troubleshoot

Inspect kubectl get externalsecret and its Ready condition. If the target is absent inspect store permissions and remote property names. After rotation verify the Secret revision, restart API and worker to reload environment variables, then check /health.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
