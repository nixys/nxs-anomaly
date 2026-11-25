# Trust a private TLS certificate authority

## Before you begin

Export the issuing CA chain in PEM format and place it in a ConfigMap or Secret in the release namespace. Check the certificate names and validity period on the provider's endpoint. [Installation options](INSTALLATION.md).

## Connect and configure

1. Set customCA to the bundle and key shown below and upgrade the chart.
2. API and worker load the bundle alongside system roots.
3. Restart pods when the bundle changes.

```yaml
customCA:
  configMapName: corp-trust-bundle
  key: trust-bundle.pem
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Verify and troubleshoot

Deliver to a test HTTPS endpoint using that CA. On unknown authority verify the mounted key and intermediate CA chain. On hostname mismatch fix the endpoint hostname or certificate. During rotation deploy the new CA before replacing the server certificate.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
