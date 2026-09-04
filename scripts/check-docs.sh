#!/usr/bin/env bash
# check-docs.sh — mechanical checks that the documentation still describes the
# code.
#
# Why this exists: four separate documentation defects were found by reading,
# not by CI, and each one was a working defect rather than a typo.
#
#   * ALERTING_RULES.md documented an ingest SLI using handler="ingest". The
#     middleware writes handler="webhook". The documented query returned no data
#     — an SLI that silently measured nothing.
#   * SECURITY.md documented keyless cosign verification long after signing had
#     moved to a key. The documented `cosign verify` was a command nobody could
#     run.
#   * MIGRATIONS.md stopped at 0017 while the directory had reached 0023.
#   * TRACING.md gave an ingest URL missing its source segment, so the copy-paste
#     smoke test 404s.
#
# Prose cannot be checked mechanically, but the four classes above are all
# *names*: metric names, table names, migration versions, file paths. Those can.
# Each check below catches one of the classes that actually bit.
#
# Usage: scripts/check-docs.sh
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

failures=0
fail() {
  printf 'check-docs FAIL  %s\n' "$1"
  failures=$((failures + 1))
}
ok() { printf 'check-docs ok    %s\n' "$1"; }

# The Russian enterprise set is the source of truth for prose: the community
# Russian set is generated from it, and the English sets are translated from it.
# Checking the source is what keeps every derived copy honest, and checking the
# English ones as they appear is what keeps a translation from outliving the
# thing it describes.
DOCS=(README.md SECURITY.md CONTRIBUTING.md CHANGELOG.md
      docs/enterprise/ru/*.md docs/community/ru/*.md
      docs/enterprise/en/*.md docs/community/en/*.md)
DOCS_RU_SRC=docs/enterprise/ru

# ── 1. Every migration has a row in docs/MIGRATIONS.md ───────────────────────
# The table drifted six versions behind the directory. Nobody notices, because a
# missing row looks exactly like a version that has not been written yet.
missing_migrations=0
for f in internal/store/migrations/*.sql; do
  version="$(basename "${f}" .sql)"
  if ! grep -q "${version}" "${DOCS_RU_SRC}/MIGRATIONS.md"; then
    fail "migration ${version} has no row in ${DOCS_RU_SRC}/MIGRATIONS.md"
    missing_migrations=$((missing_migrations + 1))
  fi
done
[ "${missing_migrations}" -eq 0 ] && ok "every migration is documented"

# ── 2. Every metric named in the docs exists in the code ─────────────────────
# Catches a renamed or removed metric leaving a dashboard panel and an alert rule
# querying a name that returns nothing. Index names (…_idx) are not metrics.
grep -rhoE 'Name:\s*"nxs_anomaly_[a-z0-9_]+"' --include=metrics.go internal/ \
  | grep -oE 'nxs_anomaly_[a-z0-9_]+' | sort -u > "${WORK}/metrics_code.txt"
grep -rhoE 'nxs_anomaly_[a-z0-9_]+' docs/prometheus-rules.yaml docs/grafana-dashboard.json "${DOCS_RU_SRC}/ALERTING_RULES.md" \
  | sed -E 's/_(bucket|sum|count)$//' \
  | grep -vE '_idx$' \
  | sort -u > "${WORK}/metrics_docs.txt"
# Recording rules define their own names (level:metric:operation), which contain
# a colon and never match the bare-name pattern above, so they need no exclusion.
if orphans="$(comm -13 "${WORK}/metrics_code.txt" "${WORK}/metrics_docs.txt")" && [ -n "${orphans}" ]; then
  while read -r m; do
    [ -n "${m}" ] && fail "metric ${m} is documented but not registered in any metrics.go"
  done <<<"${orphans}"
else
  ok "every documented metric exists in the code"
fi

# ── 3. Every nxs_anomaly_ table named in the docs exists in a migration ──────
# This one caught its own author: a migration row described
# nxs_anomaly_identity_sessions, and the table is nxs_anomaly_web_sessions.
# Both sources: the .sql files, and the migration runner itself, which creates
# nxs_anomaly_schema_migrations in Go before any .sql file runs.
grep -rhoiE 'CREATE TABLE (IF NOT EXISTS )?nxs_anomaly_[a-z0-9_]+' internal/store/migrations/ internal/store/*.go \
  | grep -oiE 'nxs_anomaly_[a-z0-9_]+' | tr 'A-Z' 'a-z' | sort -u > "${WORK}/tables_code.txt"
# Scoped to MIGRATIONS.md, where every nxs_anomaly_ identifier is either a table
# or an index — there is no prose to guess at. A looser "is this word near the
# word 'table'" heuristic was tried first and missed the very defect that
# prompted the check, so this one is exact instead of clever.
grep -ohE 'nxs_anomaly_[a-z0-9_]+' "${DOCS_RU_SRC}/MIGRATIONS.md" \
  | grep -vE '_idx$' | sort -u > "${WORK}/tables_docs.txt"
bad_tables=0
while read -r name; do
  [ -z "${name}" ] && continue
  if ! grep -qx "${name}" "${WORK}/tables_code.txt"; then
    fail "table ${name} is named in ${DOCS_RU_SRC}/MIGRATIONS.md but no migration creates it"
    bad_tables=$((bad_tables + 1))
  fi
done < "${WORK}/tables_docs.txt"
[ "${bad_tables}" -eq 0 ] && ok "every table named in MIGRATIONS.md is created by a migration"

# ── 4. Relative links in the docs point at files that exist ─────────────────
broken_links=0
for doc in "${DOCS[@]}"; do
  [ -f "${doc}" ] || continue
  base_dir="$(dirname "${doc}")"
  grep -oE '\]\([^)#][^)]*\.(md|yaml|yml|json|sql|go|sh|tpl)\)' "${doc}" 2>/dev/null \
    | sed -E 's/^\]\(//; s/\)$//' | while read -r link; do
      case "${link}" in http*|mailto:*) continue ;; esac
      target="${link%%#*}"
      [ -z "${target}" ] && continue
      if [ ! -e "${base_dir}/${target}" ] && [ ! -e "${target}" ]; then
        printf 'BROKEN|%s|%s\n' "${doc}" "${target}"
      fi
    done
done > "${WORK}/links.txt"
pending=0
while IFS='|' read -r _ doc target; do
  [ -n "${target}" ] || continue
  # A link from an English document to a sibling that exists in Russian is not
  # broken, it is not translated yet: the file lands under the same name when the
  # translation does. Counted and reported by scripts/check-docs-parity.sh rather
  # than failed here, so that a half-translated set is a number somebody watches
  # instead of a permanently red gate people learn to ignore.
  case "${doc}" in
    docs/community/en/*)
      if [ -e "docs/community/ru/${target}" ]; then
        pending=$((pending + 1))
        continue
      fi
      ;;
  esac
  fail "${doc} links to ${target}, which does not exist"
  broken_links=$((broken_links + 1))
done < "${WORK}/links.txt"
[ "${pending:-0}" = 0 ] || echo "check-docs note  ${pending} link(s) point at documents awaiting translation"
[ "${broken_links}" -eq 0 ] && ok "every relative documentation link resolves"

# ── 5. The ingest URL shape in the docs matches the routes ──────────────────
# /integrations/v1/<source>/<key>. Dropping the source segment yields a 404 that
# reads like a bad integration key, which is how it survived review.
bad_ingest=0
while IFS= read -r line; do
  [ -z "${line}" ] && continue
  bad_ingest=$((bad_ingest + 1))
  fail "ingest URL without a source segment: ${line}"
done < <(grep -rnE 'integrations/v1/(<|\$|\{|[a-z]*key)' "${DOCS[@]}" 2>/dev/null \
  | grep -vE 'integrations/v1/(webhook|alertmanager|pagerduty|victorops|grafana-alerting|opensearch|elasticsearch|legacy-pool|<source>)/' || true)
[ "${bad_ingest}" -eq 0 ] && ok "documented ingest URLs carry a source segment"

# ── 6. CI jobs named in CONTRIBUTING.md exist in .gitlab-ci.yml ─────────────
# The gate list in CONTRIBUTING.md is the first thing a contributor reads to
# learn what has to pass. It had drifted: it still called test:coverage
# informational after that job became a hard gate, and did not mention four jobs
# added since. Job *semantics* cannot be checked here, but a job named in the
# prose and absent from the pipeline can.
# sed, not tr: `tr -d ':'` would turn "test:unit:" into "testunit" and report
# every job as missing.
grep -oE '^[a-z][a-z0-9:_-]*:' .gitlab-ci.yml | sed 's/:$//' | sort -u > "${WORK}/jobs_ci.txt"
grep -ohE '`(test|deps|helm|build|release):[a-z0-9:_-]+`' CONTRIBUTING.md \
  | tr -d '`' | sort -u > "${WORK}/jobs_docs.txt"
bad_jobs=0
while read -r job; do
  [ -z "${job}" ] && continue
  if ! grep -qx "${job}" "${WORK}/jobs_ci.txt"; then
    fail "CONTRIBUTING.md names CI job ${job}, which .gitlab-ci.yml does not define"
    bad_jobs=$((bad_jobs + 1))
  fi
done < "${WORK}/jobs_docs.txt"
[ "${bad_jobs}" -eq 0 ] && ok "every CI job named in CONTRIBUTING.md exists"

echo
if [ "${failures}" -gt 0 ]; then
  echo "check-docs: ${failures} documentation defect(s)"
  echo "These are names, not prose — the code moved and the documentation did not."
  exit 1
fi
echo "check-docs: clean"
