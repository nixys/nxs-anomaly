# Service dashboard in Grafana

## Before you begin

Start with a working Prometheus scrape of the API and worker. Obtain permission to configure a Grafana data source and import dashboards. [Prometheus setup](GUIDE_PROMETHEUS.md). [Installation options](INSTALLATION.md). [Install Grafana](https://grafana.com/docs/grafana/latest/setup-grafana/installation/).

## Connect and configure

1. Add a Prometheus data source using a URL reachable from the Grafana server.
2. Save and test the connection.
3. Import the supplied dashboard JSON, select the Prometheus source and save it in the team's folder. [Dashboard JSON](../../grafana-dashboard.json).

```text
up
```

## Verify and troubleshoot

Run the query in Explore for that data source. Send a test alert and inspect ingest and delivery panels over the last 15 minutes. For No data check the selected time range, scrape targets and the source chosen at import. Retain local dashboard customizations separately before reimport.

[Deployment reference](DEPLOY.md) · [Integration catalogue](INTEGRATIONS.md)
