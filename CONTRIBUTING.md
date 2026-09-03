# Contributing

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>)?: <subject>`.

The subject should describe **what changes** in a way that's useful in `git log` six months from now. The body (separated by a blank line) should explain **why**.

### Types

- `feat` — a new user- or operator-visible feature
- `fix` — a bug fix
- `perf` — performance improvement
- `refactor` — internal restructuring without behavior change
- `test` — adding or fixing tests
- `docs` — documentation only
- `chore` — repo hygiene, tooling, dependencies (no production code)
- `ci` — CI/CD pipeline changes

### Subject

- Imperative mood ("add", not "added" / "adds")
- Lowercase, no trailing period
- Under 72 characters
- Name the **specific thing** being changed, not a generic word

### Examples

✅ Good (real commits from this repo):

```
fix: alertmanager template
fix: bulk actions (Resolve/Acknowledge/Silence) not working in Grafana
chore: repo hygiene — lint gate, dead-letter fix, cleanup
chore: remove Python artifacts and stale diagram from repo
```

❌ Avoid:

```
fix: collections             # which collections? what fix?
feat: add 1 inc              # "incremental" means nothing in log
feat: type struct            # type of what? struct for what?
feat: test for grafana       # which test? what does it cover?
feat: add Лфалф              # placeholder / typo
```

If you can't pick a specific scope, the change is probably too broad — consider splitting the commit.

### Enforcement

Commit subject **format** is checked automatically:

- **Locally** (recommended): enable the tracked hook once per clone —
  ```
  git config core.hooksPath .githooks
  ```
  The `commit-msg` hook then rejects non-conforming subjects before the commit is created.
  The same setting enables `pre-commit` and `pre-push`, which guard the release version
  (see [Releasing](#releasing)).
- **In CI**: the `test:commitlint` job validates every commit introduced by a push/MR against the same rules (`scripts/check-commit-msg.sh`).

The check enforces the *format* (`<type>(<scope>)?: <subject>`, ≤72 chars), not subject quality — a vague but well-formed subject like `fix: collections` passes the linter but still fails review. Write specific subjects.

Dependency-bot subjects (`chore(deps): …`, `fix(deps): …`) are exempt from the length limit — Renovate puts the full module path in the subject and there is nothing to shorten. The format rule still applies to them.

### Body (when to write one)

Write a body when the **why** isn't obvious from the diff. Examples of good "why" content:

- "Workaround for X bug in dependency Y"
- "Required for compatibility with Grafana plugin Z protocol"
- "Closes the race condition reported in incident 2026-05-14"
- "Restores helper-script test:integration after `services: postgres` reverted (DNS alias doesn't resolve on office-sandbox runner)"

Skip the body for self-evident changes (typo fixes, single-line refactors).

### What NOT to commit

- Local virtualenvs (`.venv*/`, `__pycache__/`)
- Editor/IDE state (`.idea/`, `.vscode/`)
- Build artifacts (`bin/`, `dist/`, `coverage.out`)
- Claude Code worktrees (`.claude/`)

These are covered by `.gitignore`. Use `git status` before staging — `git add .` and `git add -A` are easy ways to accidentally include them.

## Dev workflow

Build, vet and unit-test the whole tree:

```sh
go build ./...
go vet ./...
go test -count=1 ./...
```

Integration tests need a real PostgreSQL — the helper script spins one up in Docker:

```sh
tests/run_postgres_integration.sh
```

Linter is golangci-lint v2.6 (same as CI). Run via Docker to match the CI image exactly:

```sh
docker run --rm -v "$(pwd):/app" -w /app golangci/golangci-lint:v2.6-alpine \
  golangci-lint run --timeout=5m ./...
```

Documentation is gated too, by names rather than by prose:

```sh
scripts/check-docs.sh
```

It checks that every migration has a row in `docs/MIGRATIONS.md`, that every metric named
in the docs is registered in a `metrics.go`, that every table named in `MIGRATIONS.md` is
created by a migration, that relative links resolve, and that documented ingest URLs carry
their source segment. Each of those five corresponds to a defect that shipped and was
found by reading. It runs in `pre-commit` as well as in CI — enable the hooks once per
clone with `git config core.hooksPath .githooks`.

CI gates: `test:lint`, `test:unit`, `test:race` (race detector — the project is
concurrency-heavy), `test:govulncheck` (CVEs, via `scripts/vuln-gate.sh` and
`.vuln-allowlist.json`), `test:gosec` (security static analysis, `G104` excluded for the
deliberate `//nolint:errcheck` convention; other safe findings carry `#nosec` at the call
site), `test:fmt` (gofmt), `test:docs` (the checks above), `test:coverage` (combined
unit+integration coverage against `COVERAGE_FLOOR` — a gate, not a report), `test:kafka`
(producer round trip against a real broker), `test:integration`, `test:vet`,
`test:commitlint`, `test:version`. Nightly on a schedule: `test:loadtest` (the capacity
harness) and `deps:outdated`. The integration job runs on the `office-sandbox` runner and
uses the helper script directly (the GitLab `services: postgres` alias doesn't resolve on
that runner — don't switch it back, see `.gitlab-ci.yml` comment).

Those jobs are grouped into stages by what they cost the cluster rather than by what they
check — `static` → `quality` → `unit` → `integration` → `cluster` — so the cheap checks
answer first and the heaviest group runs alone. The three kind jobs share a
`resource_group`, as do the three database drills, which keeps them from piling up across
concurrent pipelines as well as within one. A new job belongs in the stage matching what it
needs to run: nothing, a compiler, a service container, or a cluster. Adding `needs:` to it
opts out of that ordering — which is occasionally right, and is why the two remaining uses
carry a comment saying why.

## Releasing

The release version lives in exactly one file — [`VERSION`](VERSION) — and is stamped
everywhere else from there. Never hand-edit `Chart.yaml`, `docs/openapi.json` or the chart
README version; the gates below reject a tree where they disagree.

```sh
scripts/set-version.sh 0.1.29      # stamps and re-checks
git commit -am 'chore(release): 0.1.29'
git tag v0.1.29 && git push --follow-tags
```

| Where | Form | Why |
|---|---|---|
| git tag | `v0.1.29` | what the pipeline builds and signs |
| image tags | `v0.1.29` | `${IMAGE_NAME}:${CI_COMMIT_TAG}`, prefix included |
| chart `appVersion` | `v0.1.29` | `image.tag` defaults to it, so it *is* the resolved tag |
| chart `version` | `0.1.29` | chart versions must be SemVer |
| `docs/openapi.json` `info.version` | `0.1.29` | the published contract |

Enforced by `scripts/check-version.sh` in three places: the `pre-commit` hook (the commit's
content), the `pre-push` hook (a `vX.Y.Z` tag must match the `VERSION` at the commit it
points at) and the `test:version` CI job (on a tag, against `$CI_COMMIT_TAG`).

`frontend/package.json` is deliberately outside this contract: the package is private, is
never published, and its version is mirrored in `package-lock.json`, where a mismatch
breaks `npm ci`.

A tagged pipeline then runs the release chain, modelled on
[nxs-universal-chart](https://github.com/apps/nxs-universal-chart) — one stage per step, so a
failure names the step: `release:sbom` and `release:chart:package` (package) → `release:chart:publish`
(publish) → `release:sign` (sign) → `release:verify` (verify).

Signing is **key-based** cosign, not keyless: keyless mints a Fulcio certificate from the CI's
OIDC issuer, and public Sigstore trusts `gitlab.com`, not a self-hosted `github.com`. The
release needs these CI variables: `COSIGN_PRIVATE_KEY`, `COSIGN_PUBLIC_KEY` (masked; plus
`COSIGN_PASSWORD` for a passphrase-protected key), `HARBOR_PROJECT` with
`CI_REGISTRY_USER`/`CI_REGISTRY_PASSWORD` for images, and `HARBOR_HELM_PROJECT` with
`HELM_REGISTRY_USER`/`HELM_REGISTRY_PASSWORD` for the chart. Generate the pair once and store
it as described below; images and the chart are signed **by digest**.

### Generating the release key pair

Do this once, off the runner, and keep the result in the team password manager.

```sh
curl -fsSL -o /usr/local/bin/cosign \
  https://github.com/sigstore/cosign/releases/download/v2.4.1/cosign-linux-amd64
chmod +x /usr/local/bin/cosign

export COSIGN_PASSWORD="$(openssl rand -base64 24)"   # the passphrase; keep it
cosign generate-key-pair                              # → cosign.key, cosign.pub
echo "$COSIGN_PASSWORD"

cosign public-key --key cosign.key                    # must equal cosign.pub
```

`cosign.key` is the private half, **encrypted with the passphrase** — both are needed to sign.
Neither file belongs in the repository (`.gitignore` covers them).

Then in **Settings → CI/CD → Variables** of the GitLab project:

| Variable | Value | Notes |
|---|---|---|
| `COSIGN_PRIVATE_KEY` | contents of `cosign.key` | multi-line PEM — GitLab **cannot mask** a multi-line value. Either store the PEM and mark the variable *Protected* + *Hidden*, or store `base64 -w0 cosign.key` (single line, maskable). The job accepts both forms. |
| `COSIGN_PASSWORD` | the passphrase | single line → mark *Masked* + *Protected* |
| `COSIGN_PUBLIC_KEY` | contents of `cosign.pub` | public by design; no masking needed. Same PEM-or-base64 choice. |

Mark all three *Protected* so only protected tags (`v*`) can read them, and untick "Expand
variable reference" — a PEM contains no `$`, but the setting removes a whole class of surprise.

Rotating the key means consumers must replace their pinned `cosign.pub`; announce it with the
release rather than silently re-keying.

`release:verify` closes the chain: it re-runs the chart README's own install and `cosign verify`
commands **with no registry credentials** against the artifacts just published, deriving the image
refs from the published chart so a tag the chart resolves to but nobody pushed fails the release.
Its transcript is the `release-verification.txt` artifact — that job, not the README, is the
evidence that an anonymous pull works.
