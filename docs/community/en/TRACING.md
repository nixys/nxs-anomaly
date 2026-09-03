# Distributed tracing (OpenTelemetry)

*Русская версия: [TRACING.md](../ru/TRACING.md)*

## Why, when there are already metrics and logs

The service exports some forty Prometheus metrics and structured logs. Metrics
answer "how many deliveries are slow"; logs answer "what happened inside this
process". Neither answers the question a responder asks after an incident:
**why did this particular notification go out four minutes late?**

The answer is a causal chain: ingest → grouping → escalation step → provider
call. Metrics aggregate it away, logs record the links but not the connections
between them. That chain is the trace.

What is peculiar to this service: the chain **crosses process and time
boundaries**. An API pod accepts the webhook; a worker pod runs the escalation
and the delivery minutes later, in a different cycle. `X-Request-ID` cannot join
them — they are different requests. A trace id can.

## Turning it on

Tracing is off until an endpoint is set. Without one, `Init` installs neither an
exporter nor a provider: spans become no-ops on the global provider, and the cost
is one interface call on paths that were doing work anyway. You do not need to
run a collector in order to run nxs-anomaly.

| Variable | |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | base URL of an OTLP/HTTP collector, e.g. `http://otel-collector:4318`; the SDK appends `/v1/traces` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | the same, for traces only; takes precedence |
| `OTEL_SERVICE_NAME` | the `service.name` value; defaults to `nxs-anomaly` |
| `OTEL_TRACES_SAMPLER_ARG` | sampling ratio `0..1`; empty means sample everything |
| `OTEL_EXPORTER_OTLP_HEADERS` | headers for the collector (a token, say) — the SDK's standard variable |

In the Helm chart:

```yaml
tracing:
  enabled: true
  endpoint: http://otel-collector.observability:4318
  serviceName: nxs-anomaly-prod   # optional
  sampleRatio: ""                 # empty = everything
```

The configuration goes into the shared ConfigMap, which means **both the API and
the worker**. That matters: a trace produced by only half of a deployment is
worse than none, because it looks complete. `tracing.enabled: true` without an
`endpoint` is a render error rather than a deployment that quietly exports into
nowhere.

### Why everything is sampled by default

Always-on sampling is usually a reckless default. Here it is not. This service's
request volume is the rate at which somebody else's infrastructure breaks, not
user traffic: spans arrive in the tens per second. And the valuable ones are
exactly the rare slow traces that a ratio sampler discards first. Set a ratio
only if the volume genuinely demands it.

An upstream service's decision is respected: the sampler is `ParentBased`.

## What is instrumented

| Span | Where | Key attributes |
|---|---|---|
| `<METHOD> <category>` | HTTP middleware, the request's root span | `url.path`, `http.response.status_code`, `client.address` |
| `ingest.alert` | `engine.IngestAlert` | `nxs.source`, `nxs.alert_group_id` |
| `worker.cycle` | `engine.RunWorkerCycle` | `nxs.stage.<stage>_ms` per stage, plus group and notification counters |
| `delivery.<channel>` | `engine.deliverNotificationViaAdapter` | `nxs.notification_id`, `nxs.channel`, `nxs.alert_group_id`, `nxs.retry_count`, `nxs.outcome`, `nxs.provider_status`; and a **link** to the ingest trace |

### How a trace survives a write to the database

An alert's life is not one call chain. An API pod accepts the webhook and
answers; minutes later a worker pod picks the group out of a table and delivers
it. There is no call between them — only a row — so no amount of passing
`context.Context` around will join the two.

The solution is the one used for queues: on the way in, the span context is
serialised into a string (`trace_parent` in the alert group's JSONB, in the
`Extra` field — no migration, because nothing searches or indexes on it), and on
the way out it is attached to the delivery span as a **link**. The field is
written only while tracing is on, so the canonical shape of a group is unchanged
for everybody else.

The whole path: `ingest.alert` → `trace_parent` on the group → `trace_parent` in
the notification payload → `tracing.StartLinked` in
`deliverNotificationViaAdapter`. A group created before tracing was turned on
simply has no field, and its delivery gets an ordinary span with no link — no
branching at the call site.

Deliberate omissions:

- **`/live`, `/health`, `/ready` and `/metrics` are not traced.** The kubelet and
  Prometheus poll them forever; a span per liveness probe is pure noise that
  would crowd out the traces this was built for.
- **A span is named after the route's category, not its path.** Paths carry
  entity identifiers; otherwise every alert group would produce its own unique
  span name and grouping in the tracing backend would stop working. The full path
  stays as the `url.path` attribute.
- **Worker cycle stages have no child spans** — only durations as attributes. The
  cycle always runs the same fixed sequence, so a dozen child spans per tick
  would say exactly what the durations already say while multiplying the span
  volume of an idle deployment by the tick rate.
- **A failed delivery is marked with an `Error` status but not `RecordError`.**
  It is an expected state of the world, not a bug.

## How the trace connects to everything else

The trace id is the joining key, and it is put where it outlives the trace
itself:

- **`X-Trace-ID` on the response.** Somebody complaining that "my webhook was
  slow" can name an id rather than the time they thought it happened.
- **Logs.** The access log (`http_request`) and `delivery_attempt` both carry
  `trace_id`.
- **The audit trail.** A `trace_id` column (migration 0025) beside the
  `request_id` from 0022, and a filter: `GET /api/v1/audit?trace_id=...`.

The last one is not duplication. Spans age out of a collector in days or weeks,
while an audit record lives as long as retention says. Finding a row six weeks
later and seeing a trace id in it is how "who acknowledged this, and what was the
system doing at the time" stays answerable after the spans are gone.

Traces are a copy of operational data outside PostgreSQL. A user erasure cannot
remove them from an external collector or backend; set retention and access there
separately. Attributes must not carry alert payloads, tokens or delivery
addresses. The full map of these boundaries is in
[DATA_INVENTORY.md](DATA_INVENTORY.md).

## Limits

- **The delivery span is joined to ingest by a link, not by parentage.** That is
  a choice, not an omission. Parentage would mean a four-minute span sitting
  inside an HTTP request that finished in twenty milliseconds, and a delivery
  leaving the trace of its own worker cycle. A link keeps both readings: the
  delivery belongs to its cycle, and "what caused it" is one hop away. In a
  tracing UI it appears under Links rather than as nesting.
- **The SSRF guard does not apply to the exporter.**
  `NXS_ANOMALY_BLOCK_PRIVATE_WEBHOOKS` protects delivery addresses; the collector
  is your own address, usually a private in-cluster one. That is intended.
- **A failure to build the exporter is not fatal.** `tracing_exporter_failed` is
  logged and the service starts. A service that refuses to come up because a
  trace collector is missing has turned an observability problem into an outage.
- Problems inside the SDK surface as `otel_error` in the log — without that, a
  collector rejecting every batch looks exactly like a service producing no
  spans.

## Checking it works

```bash
# a collector that simply prints whatever it receives
docker run --rm -p 4318:4318 otel/opentelemetry-collector:latest

export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
./nxs-anomaly serve
```

`tracing_enabled` appears in the startup log. Then send an alert and look at
`X-Trace-ID` on the response:

```bash
curl -si -X POST localhost:8080/integrations/v1/webhook/<key> \
  -d '{"title":"probe","severity":"warning"}' | grep -i x-trace-id
```
