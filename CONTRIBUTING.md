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

✅ Good:

```
fix: alertmanager template
fix: bulk actions (Resolve/Acknowledge/Silence) not working in Grafana
chore: repo hygiene — lint gate, dead-letter fix, cleanup
```

❌ Avoid:

```
fix: collections             # which collections? what fix?
feat: add 1 inc              # "incremental" means nothing in log
feat: type struct            # type of what? struct for what?
```

If you can't pick a specific scope, the change is probably too broad — consider splitting the commit.

## Dev workflow

Build, vet, race and unit-test the whole tree:

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
```

Integration tests need a real PostgreSQL — the helper script spins one up in Docker:

```sh
tests/run_postgres_integration.sh
```

Linter is golangci-lint v2.6. Run via Docker to match CI exactly:

```sh
docker run --rm -v "$(pwd):/app" -w /app golangci/golangci-lint:v2.6-alpine \
  golangci-lint run --timeout=15m ./...
```

Documentation is gated too, by names rather than by prose:

```sh
scripts/check-docs.sh
```

It checks that every migration has a row in `docs/community/en/MIGRATIONS.md`, that every
metric named in the docs is registered in a `metrics.go`, that every table it names is
created by a migration, and that relative links resolve.

Frontend (Node.js 22.12+, from `frontend/`):

```sh
npm ci
npm run typecheck
npm test
npm run build
```

Vulnerability gates, if you touch dependencies:

```sh
GOVULNCHECK=$(go env GOPATH)/bin/govulncheck scripts/vuln-gate.sh   # go install golang.org/x/vuln/cmd/govulncheck@v1.7.0 first
cd frontend && node scripts/audit-gate.mjs --level=high --omit-dev
```

`.github/workflows/ci.yml` runs all of the above — `go`, `lint`, `govulncheck`,
`postgres-integration`, `docs`, `frontend`, `helm` — on every push to `main` and every pull
request, including from forks (no publish credentials are needed or granted for it).
`.github/workflows/security-nightly.yml` reruns the two dependency gates on a schedule,
independent of pushes.

### What NOT to commit

- Editor/IDE state (`.idea/`, `.vscode/`)
- Build artifacts (`bin/`, `dist/`, `coverage.out`, `node_modules/`)
- Your local `.env` — copy `.env.example` instead; `.gitignore` covers it

Use `git status` before staging — `git add .` and `git add -A` are easy ways to
accidentally include them.

## Releasing

This repository does not cut its own releases. It is the published mirror of an internal
one: a pull request merged here is applied upstream by a maintainer, and it ships in the
next release commit with you credited as co-author — see the root [README](README.md#contributing).
That upstream pipeline pushes the tagged, generated tree here, and pushing the tag is what
triggers `.github/workflows/release.yml`, which builds, signs (keyless cosign) and publishes
the images and Helm chart from *this* checkout — see [SECURITY.md](SECURITY.md#supply-chain)
for what "signed" means and how to verify it. Nothing in that chain needs a contributor to
do anything beyond opening the pull request.
