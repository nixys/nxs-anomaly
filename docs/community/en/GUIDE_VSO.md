# Vault Secrets Operator

## Before you begin

Install HashiCorp Vault Secrets Operator and configure a VaultConnection and VaultAuth for the target namespace. Grant its role read access to a KV path containing keys named after application environment variables, including NXS_ANOMALY_DB_DSN. [Installation options](INSTALLATION.md). [Install Vault Secrets Operator](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/vso/installation).

## Connect and configure

1. Enable vaultSecretOperator as the only primary secret source and point it at the existing mount, path and auth reference.
2. Apply the release.
3. Check the chart-created VaultStaticSecret and its destination Secret.

```yaml
vaultSecretOperator:
  enabled: true
  mount: kv
  type: kv-v2
  path: nxs-anomaly
  vaultAuthRef: app-auth
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

Inspect the synchronization resource and obtain the name of its destination. Use operator events to distinguish authentication failures from a missing KV path.

```bash
kubectl -n "$NS" get vaultauth,vaultstaticsecret
kubectl -n "$NS" describe vaultstaticsecret "$NAME"
kubectl -n "$NS" get vaultstaticsecret "$NAME" -o jsonpath='{.spec.destination.name}'
```

## Verify and troubleshoot

Inspect kubectl describe vaultstaticsecret and the operator events. On permission denied verify Vault policy, role binding and mount type. After credential rotation wait for refreshAfter, restart consumers of environment secrets and verify connectivity.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
