# Support

Open an issue: <https://github.com/nixys/nxs-anomaly/issues>.

There is no service-level promise attached to it. Nobody is on call for this
tracker, and a report may wait. Saying so plainly is more useful than an implied
promise that goes unmet: if you need a response inside a known time, that is what
the enterprise edition's support covers.

## What helps a report get answered

- The version, from `/health`, and the edition it reports.
- How it is deployed — compose, the Helm chart, or something of your own.
- What you did, what happened, and what you expected instead.
- The relevant log lines. The service logs structured events; `ingest_failed`,
  `delivery_failed` and `worker_cycle_complete` are usually the interesting ones.

## Supported surface

Covered: the API and worker, the web interface, the container images, and the
Helm chart installed with an ingress and either the bundled PostgreSQL or an
external one.

Best effort: Istio, the Kubernetes Gateway API, Vault and External Secrets. The
chart supports them and they are exercised in CI, but a problem specific to one
of them may be answered with a pointer rather than a fix.

Not covered here: anything about the enterprise edition, and security
vulnerabilities — those go to the address in [SECURITY.md](SECURITY.md), never to
a public issue.
