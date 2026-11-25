# Publish with Kubernetes Gateway API

## Before you begin

Install the Gateway API CRDs and controller and prepare a Gateway with a listener for the hostname. Its allowedRoutes must permit routes from the application namespace. Configure DNS and TLS on the gateway. [Installation options](INSTALLATION.md). [Install Gateway API](https://gateway-api.sigs.k8s.io/guides/).

## Connect and configure

1. Enable the chart HTTPRoute and set parentRefs and hostnames as shown below.
2. Set the parent namespace explicitly for a shared gateway.
3. Apply the release and inspect the route status.

```yaml
gatewayAPI:
  httpRoute:
    enabled: true
    parentRefs: [{name: public, namespace: gateways}]
    hostnames: [oncall.example.org]
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Operational check

Set ROUTE to the HTTPRoute name and NS to its namespace. Inspect each parent independently on installations with several listeners.

```bash
kubectl -n "$NS" get httproute "$ROUTE" -o json | jq '.status.parents[] | {parentRef, conditions}'
```

## Verify and troubleshoot

Check Accepted and ResolvedRefs on the route and send a test alert through HTTPS. If it is unattached check allowedRoutes and listener hostname. For unresolved backend references inspect the Service and any cross-namespace ReferenceGrant.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
