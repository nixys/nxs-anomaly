# Publish through Kubernetes Ingress

## Before you begin

Install an ingress controller with a known IngressClass. Point the service hostname at its external address and create a TLS Secret with a matching certificate in the release namespace. [Installation options](INSTALLATION.md). [Choose an Ingress controller](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/).

## Connect and configure

1. Enable ingress in the chart, set the class, hostname and TLS secret below.
2. Keep the frontend enabled for the chart route and ensure NetworkPolicy allows traffic from the controller.
3. Apply the release.

```yaml
ingress:
  enabled: true
  className: nginx
  host: oncall.example.org
  tls:
    - hosts: [oncall.example.org]
      secretName: oncall-tls
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

Set HOST to the public DNS name, then check that the HTTPS path reaches the application and its database.

```bash
kubectl -n "$NS" get ingress,service,endpointslice
curl --fail-with-body "https://$HOST/health"
```

## Verify and troubleshoot

Inspect kubectl get ingress and open the HTTPS UI. Verify API access through the same host with a test webhook. For a default backend or 404 compare the request Host, IngressClass and backend service. Renew the TLS certificate before expiry.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
