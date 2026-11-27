# Export traces over OTLP/HTTP

## Before you begin

Prepare a collector with an OTLP HTTP receiver, a traces pipeline and a reachable tracing backend. Verify routing and trust from API and worker to the collector. Obtain collector credentials if required. [Installation options](INSTALLATION.md). [Install the Collector](https://opentelemetry.io/docs/collector/installation/).

## Connect and configure

1. Set the values below on both processes.
2. The SDK appends /v1/traces to the base URL.
3. For a full URL use `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` instead.
4. Set `OTEL_EXPORTER_OTLP_HEADERS` in the Secret for authentication, then restart both processes.

```dotenv
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_SERVICE_NAME=nxs-anomaly
OTEL_TRACES_SAMPLER_ARG=1
```

Pass these settings using the service environment. With Helm use extraConfig for non-secret values and the application Secret for credentials. Recreate the affected API and worker processes after changing their environment.

## Verify and troubleshoot

Send a test alert that produces a delivery. Search the backend by service.name and follow the ingest and linked delivery spans. If only half appear, inspect both deployments and collector exports. After commissioning choose a sampling ratio suitable for traffic.

[Sampling, collector settings and verification](TRACING.md) · [Integration catalogue](INTEGRATIONS.md)
