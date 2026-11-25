# Scrape API and worker metrics

## Before you begin

Prepare Prometheus with network access to the API and worker metrics listeners. For the Kubernetes option install Prometheus Operator and make sure the Prometheus instance selects monitors and rules from the release namespace. [Installation options](INSTALLATION.md). [Install Prometheus Operator](https://prometheus-operator.dev/docs/getting-started/installation/).

## Connect and configure

1. Enable both chart resources below.
2. Add labels required by the Prometheus selectors and allow scrape traffic through NetworkPolicy.

For Prometheus without the Operator, add the API and worker addresses to `scrape_configs`. Prometheus scrapes `/metrics` by default. Save the example with the top-level `groups` key as a separate file and list its path under `rule_files`.

```yaml
serviceMonitor:
  enabled: true
prometheusRule:
  enabled: true
```

Merge the fragment into your release values. Keep the existing database, secret and image settings. From the checkout for the installed chart version, set RELEASE and NS and apply the complete values file.

```bash
helm upgrade "$RELEASE" deploy/helm/nxs-anomaly -n "$NS" -f values.yaml --wait
```

## Verify and troubleshoot

Check that both targets are UP, query up for each target and open the Prometheus Rules page. When both targets disappear after upgrade, inspect ServiceMonitor selectors and namespace selection. A DOWN target calls for checking the service port and network policy.

[Alerting rules and scrape configuration](ALERTING_RULES.md) · [Integration catalogue](INTEGRATIONS.md)
