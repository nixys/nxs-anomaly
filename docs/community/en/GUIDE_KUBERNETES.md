# Install the Helm integration

## Before you begin

Prepare kubectl access to the target cluster, Helm, the chart for your edition, a namespace and a PostgreSQL database. Create the application environment Secret in that namespace with `NXS_ANOMALY_DB_DSN` and application credentials. [Installation options](INSTALLATION.md). [Install Helm](https://helm.sh/docs/intro/install/).

## Connect and configure

1. From the checkout root set RELEASE and NS to the target release and namespace.
2. In values.yaml enable existingSecret and set its name.
3. Add frontend ingress or use port forwarding for a local check.
4. Render first, inspect resources and install.

```bash
helm template "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml
helm upgrade --install "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
helm test "$RELEASE" -n "$NS"
```

## Verify and troubleshoot

Check pod readiness, /health and one test alert through the worker. Use kubectl describe pod for scheduling or mount failures and inspect API and worker logs for migration or credential errors. Keep versioned values for the next release.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
