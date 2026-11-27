# Route traffic through Istio

## Before you begin

Install Istio with an ingress gateway. Configure its listener, TLS credential and public DNS. Obtain the namespace/name of the Gateway that accepts the application hostname. [Installation options](INSTALLATION.md). [Install Istio](https://istio.io/latest/docs/setup/).

## Connect and configure

1. Enable the chart VirtualService and attach it to the existing Gateway as below.
2. Match its hosts to the listener and DNS name.
3. Apply the changes and inspect the generated VirtualService destination.

```yaml
istio:
  virtualService:
    enabled: true
    gateways: [istio-system/public]
    hosts: [oncall.example.org]
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

With istioctl configured for the cluster, check for missing gateways, invalid hosts and services before changing the routing rules.

```bash
istioctl analyze -n "$NS"
kubectl -n "$NS" get virtualservice -o wide
```

## Verify and troubleshoot

Open the external URL and send a test webhook through the gateway. For 404 inspect hosts and gateway binding; for 503 check destination endpoints and mesh policies. Recheck routing after gateway or certificate rotation.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
