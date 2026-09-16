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

This policy covers nxs-anomaly Community. The first stable release is `v1.0.0`.
Security fixes are developed on `main` and published in a new stable release.
Use the latest stable release listed on the [Releases page](https://github.com/nixys/nxs-anomaly/releases).

| Version or branch | Security maintenance |
|---|---|
| Latest stable release | Supported; fixes are delivered in a subsequent stable release |
| Older stable releases | Not maintained; upgrade to the latest stable release |
| Pre-1.0 releases (`0.x`) | Not maintained |
| Prereleases | Evaluation only; upgrade to a stable release when available |
| `main` | Development branch where fixes are integrated; not a supported release |

There are no separately maintained LTS branches or guaranteed backports to older
release lines. An affected older installation may need an upgrade to receive a fix.
Published tags and images are not replaced with patched contents under the same version.

Before upgrading, read the release notes and [migration guidance](docs/community/en/MIGRATIONS.md),
back up PostgreSQL and retain your configuration. Upgrade the API, worker and
frontend together using matching application versions, then verify notification
delivery. Database migrations are forward-only; reverting images alone may not
undo an upgrade.

The Helm chart has its own chart version and identifies the application version
through `appVersion`. The [Terraform provider](https://github.com/nixys/terraform-provider-nxs-anomaly)
is released separately; its version number does not need to match the application.
Check the relevant release notes before changing either component.

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
RELEASE_TAG='REPLACE_WITH_RELEASE_TAG'
CHART_VERSION="${RELEASE_TAG#v}"
cosign verify \
  --certificate-identity-regexp '^https://github.com/nixys/nxs-anomaly/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "ghcr.io/nixys/nxs-anomaly:${RELEASE_TAG}"

cosign verify \
  --certificate-identity-regexp '^https://github.com/nixys/nxs-anomaly/\.github/workflows/release\.yml@refs/tags/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "ghcr.io/nixys/nxs-anomaly:${CHART_VERSION}"
```

Add `verify-attestation --type cyclonedx` in place of `verify` for the SBOM attestation.
Keyless works here — and would not for a self-hosted CI system — because public Sigstore's
trust root covers `github.com`'s OIDC issuer. The `verify` job in `release.yml` re-runs both
commands above against the just-published artifacts **with no registry credentials**, so a
release cannot go green with a signature that does not check out.

### Helm chart provenance

The chart carries a **second, independent** signature: Helm's own GPG provenance
(`.prov`), pushed as an extra layer of the same OCI artifact next to the chart. It exists
because Artifact Hub's "Signed" badge and `helm pull --verify` both speak this format, not
Sigstore — the cosign signature above proves *this workflow, from this repository, built
it*; this one proves *the key named in [`Chart.yaml`](deploy/helm/nxs-anomaly/Chart.yaml)'s
`artifacthub.io/signKey` blessed these exact bytes*. Neither substitutes for the other.

```bash
gpg --import assets/nxs-anomaly-helm-signing.pub.asc
gpg --export D89A6071DDBD8F391414734A64ABC61D62A7FFE1 > /tmp/nxs-anomaly-helm-keyring.gpg
helm pull oci://ghcr.io/nixys/nxs-anomaly --version "$CHART_VERSION" \
  --verify --keyring /tmp/nxs-anomaly-helm-keyring.gpg
```

**Generating and rotating the key** (maintainer runbook; do this once, off the runner, and
keep the result in the team password manager — never in this repository):

```sh
gpg --batch --pinentry-mode loopback --passphrase-fd 0 --quick-gen-key \
  "Nixys Team (nxs-anomaly Helm chart signing)" rsa4096 sign never <<< "$(openssl rand -base64 24)"
# ^ prompts nothing further; the passphrase above is generated, not chosen — save it now,
# it is not recoverable from the key material.

FPR="$(gpg --list-secret-keys --with-colons "nxs-anomaly Helm chart signing" \
  | awk -F: '/^fpr:/ { print $10; exit }')"
gpg --armor --export "$FPR" > nxs-anomaly-helm-signing.pub.asc
gpg --export-secret-keys "$FPR" | base64 -w0 > nxs-anomaly-helm-signing.key.b64
```

Then:

1. Commit `nxs-anomaly-helm-signing.pub.asc` as `packaging/community/assets/nxs-anomaly-helm-signing.pub.asc`
   (it ships into the public repo verbatim — this is the file the verify commands above import).
2. Set `artifacthub.io/signKey` in `deploy/helm/nxs-anomaly/Chart.yaml` to:
   ```yaml
   artifacthub.io/signKey: |
     fingerprint: <FPR from above>
     url: https://raw.githubusercontent.com/nixys/nxs-anomaly/main/assets/nxs-anomaly-helm-signing.pub.asc
   ```
3. In the GitHub repository's **Settings → Secrets and variables → Actions**, set
   `HELM_GPG_PRIVATE_KEY` to the contents of `nxs-anomaly-helm-signing.key.b64` and
   `HELM_GPG_PASSPHRASE` to the passphrase saved in step 1. Both are read only by
   `release.yml`'s `chart` job.
4. Delete the local `.key.b64`/passphrase copies once they are in the password manager and
   GitHub; the private key must not exist anywhere but those two places.

Rotation is the same sequence with a new key: `artifacthub.io/signKey` and the GitHub secrets
move together in one release, and Artifact Hub simply starts reporting the new fingerprint —
older, already-published chart versions keep verifying against the key that actually signed them.

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
