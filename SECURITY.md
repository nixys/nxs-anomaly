# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately, **not** through public GitHub
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
(see [SECURITY_PROFILE.md](docs/community/en/SECURITY_PROFILE.md)):

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

Tagged release images and the OCI Helm chart are published from `.github/workflows/release.yml`
with a CycloneDX SBOM and a **keyless** cosign signature — there is no key to obtain or pin.
The signature carries the workflow that produced it, tied to this repository and the tag:

```bash
cosign verify \
  --certificate-identity-regexp '^https://github.com/nixys/nxs-anomaly/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/nixys/nxs-anomaly:<tag>

cosign verify \
  --certificate-identity-regexp '^https://github.com/nixys/nxs-anomaly/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/nixys/nxs-anomaly:<chart-version>
```

Add `verify-attestation --type cyclonedx` in place of `verify` for the SBOM attestation.
Keyless works here — and would not for a self-hosted CI system — because public Sigstore's
trust root covers `github.com`'s OIDC issuer. The `verify` job in `release.yml` re-runs both
commands above against the just-published artifacts **with no registry credentials**, so a
release cannot go green with a signature that does not check out.

### Dependency policy

Two gates, one per ecosystem, built the same way:

| | Go | Frontend |
|---|---|---|
| Scanner | `govulncheck` | `npm audit` |
| Gate | `scripts/vuln-gate.sh` | `frontend/scripts/audit-gate.mjs` |
| Allowlist | `.vuln-allowlist.json` | `frontend/.audit-allowlist.json` |
| Workflow job | `ci.yml` → `govulncheck` | `ci.yml` → `frontend` |

Both fail on any unaccepted finding, and both fail when an accepted one is past its
`reviewBy` date — an exception that nobody re-reads is a silence, not a decision. Every
allowlist entry must state **why this deployment is not affected**, not that a fix is
inconvenient.

The Go gate distinguishes what `govulncheck` distinguishes: it blocks on symbol-level
findings (code this project actually calls) and reports module-level ones (present in the
dependency tree, never reached) as informational. Gating on the wider set would mean
failing the build over code that provably cannot execute here, which is how a security
gate gets muted.

Both scanners also run on a nightly schedule (`.github/workflows/security-nightly.yml`),
independently of any push to the repository — new advisories are published on their own
timetable, and a nightly failure opens or updates a tracking issue rather than paging
anyone.

### Vendored third-party code

Go dependencies are vendored (`vendor/`) so a build is reproducible without the module
proxy.
