# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately, **not** through public GitLab/GitHub
issues.

- Email: **security@nixys.io** (PGP key on request).
- Include: affected version/commit, a description, and a minimal reproduction if possible.
- We acknowledge within **3 business days** and aim to provide a remediation plan
  within **10 business days**, coordinating disclosure with you.

Please do not run denial-of-service tests, access data that is not yours, or otherwise
degrade a running service while researching.

## Supported versions

nxs-anomaly is pre-1.0. Security fixes are applied to the latest tagged release and
`main`. Older tags are not maintained.

## Hardening — the production profile

Run with `NXS_ANOMALY_PROFILE=production` to enable the hardened defaults in one place
(see [docs/community/en/SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md)):

- SSRF guard on (outbound delivery refuses private/loopback/link-local hosts);
- request rate limits on (ingest and API);
- delivery circuit breaker on;
- new inline secrets refused — secrets must be `env:VAR` references.

Any single default can be overridden with its explicit env var.

## Secrets

- Provider credentials (Telegram/SMTP/Asterisk) are read from the environment, never
  stored in the database.
- Per-object secrets (integration `webhook_secret`, chatops `webhook_url`, mobile
  `push_token`) may be stored as `env:VAR` references so the plaintext never lands in
  PostgreSQL. Reads mask credential-shaped fields regardless.
- The chart never bakes plaintext secrets by default; it references an existing Secret,
  External Secrets, or the Vault Secrets Operator (see `deploy/helm/nxs-anomaly`).

## Supply chain

### Verifying a release

Tagged release images and the OCI Helm chart are published with a CycloneDX SBOM and a
**key-based** cosign signature. Verify with the project's public key:

```bash
cosign verify --key cosign.pub <image>
cosign verify --key cosign.pub <oci-chart-ref>:<version>
```

Signing is deliberately key-based rather than keyless. Keyless mints a Fulcio certificate
from the CI's OIDC issuer, and the public Sigstore trust root covers `gitlab.com`, not a
self-hosted instance like `github.com` — a keyless signature from here would be one
nobody could verify. The `release:verify` stage re-runs both commands above against the
just-published artifacts **with no registry credentials**, so a release cannot go green
with a signature that does not check out.

### Dependency policy

Two gates, one per ecosystem, built the same way:

| | Go | Frontend |
|---|---|---|
| Scanner | `govulncheck` | `npm audit` |
| Gate | `scripts/vuln-gate.sh` | `frontend/scripts/audit-gate.mjs` |
| Allowlist | `.vuln-allowlist.json` | `frontend/.audit-allowlist.json` |
| CI job | `test:govulncheck` | `test:frontend:audit` |

Both fail on any unaccepted finding, and both fail when an accepted one is past its
`reviewBy` date — an exception that nobody re-reads is a silence, not a decision. Every
allowlist entry must state **why this deployment is not affected**, not that a fix is
inconvenient.

The Go gate distinguishes what `govulncheck` distinguishes: it blocks on symbol-level
findings (code this project actually calls) and reports module-level ones (present in the
dependency tree, never reached) as informational. Gating on the wider set would mean
failing the build over code that provably cannot execute here, which is how a security
gate gets muted.

Both scanners also run on the nightly schedule, alongside `deps:outdated`. Advisories are
published on their own timetable, and a repository that goes three quiet weeks would
otherwise go three weeks unscanned.

### Vendored third-party code

Go dependencies are vendored (`vendor/`) so a build is reproducible without the module
proxy. The dev stack also carries a Grafana-signed plugin under `dev/grafana/plugins/`;
its `MANIFEST.txt` is a signed file list and must stay intact — see the README there
before deleting anything from it.
