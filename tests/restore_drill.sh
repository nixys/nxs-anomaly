#!/usr/bin/env bash
# Automated PostgreSQL backup/restore drill for nxs-anomaly (BETA-041).
#
#   seed a complete alert flow -> pg_dump (backup) -> DROP the database
#   (disaster) -> restore from the dump -> verify the data survived -> bring the
#   real API and worker up on the restored database and wait until /readiness
#   says the service can page someone -> prove the previous release still runs
#   against the restored schema.
#
# RTO is measured to **service readiness**, not to the end of the database
# import. A restored database nobody can be paged from is not a recovered
# service, and the gap between the two (binary start, migrations, worker
# heartbeat) is exactly where a real recovery overruns its budget.
#
# Runs against its own throwaway postgres container by default. In CI, where the
# database is a service and docker-in-docker is unavailable, set
# NXS_ANOMALY_TEST_DATABASE_URL and NXS_ANOMALY_DRILL_PSQL/NXS_ANOMALY_DRILL_DUMP
# to psql/pg_dump commands that reach it.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

IMAGE="${NXS_ANOMALY_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
PORT="${NXS_ANOMALY_TEST_POSTGRES_PORT:-55444}"
DB="${NXS_ANOMALY_TEST_POSTGRES_DB:-nxs_anomaly_drill}"
USER="${NXS_ANOMALY_TEST_POSTGRES_USER:-nxs_anomaly}"
PASSWORD="${NXS_ANOMALY_TEST_POSTGRES_PASSWORD:-nxs_anomaly}"
CONTAINER="${NXS_ANOMALY_TEST_POSTGRES_CONTAINER:-nxs-anomaly-drill-$$}"
RTO_BUDGET_SECONDS="${NXS_ANOMALY_DRILL_RTO_BUDGET:-1800}"   # 30 min beta target
BACKUP_FILE="$(mktemp -t nxs-anomaly-drill.XXXXXX.sql)"

export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOMODCACHE="${GOMODCACHE:-/tmp/go-mod}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"

API_PORT="${NXS_ANOMALY_DRILL_API_PORT:-18080}"
WORKER_PORT="${NXS_ANOMALY_DRILL_WORKER_PORT:-18081}"
PREV_PORT="${NXS_ANOMALY_DRILL_PREV_PORT:-18082}"
SERVICE_TIMEOUT="${NXS_ANOMALY_DRILL_SERVICE_TIMEOUT:-180}"
DRILL_API_KEY="drill-key-$$"
WORK_DIR="$(mktemp -d -t nxs-anomaly-drill-work.XXXXXX)"
PREV_WORKTREE="${WORK_DIR}/prev"

STARTED_CONTAINER=0
API_PID=""
WORKER_PID=""
PREV_PID=""
cleanup() {
  for pid in "${API_PID}" "${WORKER_PID}" "${PREV_PID}"; do
    [ -n "${pid}" ] && kill "${pid}" >/dev/null 2>&1 || true
  done
  [ -d "${PREV_WORKTREE}" ] && git worktree remove --force "${PREV_WORKTREE}" >/dev/null 2>&1 || true
  [ "${STARTED_CONTAINER}" = "1" ] && [ "${NXS_ANOMALY_KEEP_TEST_POSTGRES:-0}" != "1" ] && docker stop "${CONTAINER}" >/dev/null 2>&1 || true
  rm -rf "${BACKUP_FILE}" "${WORK_DIR}"
}
trap cleanup EXIT

# Poll an HTTP endpoint until its body matches a pattern, or fail after the
# service timeout. Every wait in this drill is bounded: a recovery that hangs
# must fail the drill, not sit until the CI job times out.
wait_for() {
  local what="$1" url="$2" pattern="$3" header="${4:-}" deadline
  deadline=$(( $(date +%s) + SERVICE_TIMEOUT ))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    if [ -n "${header}" ]; then
      body="$(curl -fsS -H "${header}" "${url}" 2>/dev/null || true)"
    else
      body="$(curl -fsS "${url}" 2>/dev/null || true)"
    fi
    case "${body}" in
      *"${pattern}"*) return 0 ;;
    esac
    sleep 1
  done
  echo "DRILL FAILED: ${what} did not become ready within ${SERVICE_TIMEOUT}s"
  echo "   last response from ${url}: ${body:-<none>}"
  return 1
}

# dump/restore/reset default to the throwaway container; overridable for CI.
DUMP_CMD="${NXS_ANOMALY_DRILL_DUMP:-docker exec ${CONTAINER} pg_dump -U ${USER} -d ${DB}}"
PSQL_CMD="${NXS_ANOMALY_DRILL_PSQL:-docker exec -i ${CONTAINER} psql -v ON_ERROR_STOP=1 -U ${USER} -d ${DB}}"
RESET_CMD="${NXS_ANOMALY_DRILL_RESET:-docker exec ${CONTAINER} psql -v ON_ERROR_STOP=1 -U ${USER} -d postgres -c}"

if [ -z "${NXS_ANOMALY_TEST_DATABASE_URL:-}" ]; then
  echo "== starting throwaway postgres (${IMAGE}) =="
  docker run --name "${CONTAINER}" \
    -e "POSTGRES_DB=${DB}" -e "POSTGRES_USER=${USER}" -e "POSTGRES_PASSWORD=${PASSWORD}" \
    -p "127.0.0.1:${PORT}:5432" -d "${IMAGE}" >/dev/null
  STARTED_CONTAINER=1
  # -h 127.0.0.1 on purpose: the entrypoint runs initdb against a temporary
  # server that listens on the unix socket only, so a socket pg_isready reports
  # "ready" and then the connection dies when that server is restarted.
  for _ in $(seq 1 60); do
    docker exec "${CONTAINER}" pg_isready -h 127.0.0.1 -U "${USER}" -d "${DB}" >/dev/null 2>&1 && break
    sleep 1
  done
  docker exec "${CONTAINER}" pg_isready -h 127.0.0.1 -U "${USER}" -d "${DB}" >/dev/null
  export NXS_ANOMALY_TEST_DATABASE_URL="postgres://${USER}:${PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
fi

echo "== 1/6 seed a complete alert flow (migrations run on store open) =="
go test -tags restoredrill -count=1 -run TestRestoreDrillSeed ./tests/ -v 2>&1 | grep -E "seeded|PASS|FAIL|ok" || true

echo "== 2/6 backup (pg_dump) — this timestamp is the recovery point (RPO) =="
RPO_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
${DUMP_CMD} > "${BACKUP_FILE}"
echo "   backup taken at ${RPO_AT}, $(wc -c < "${BACKUP_FILE}") bytes"

echo "== 3/6 DISASTER: drop and recreate the database, then restore =="
RESTORE_START=$(date +%s)
${RESET_CMD} "DROP DATABASE ${DB} WITH (FORCE);"
${RESET_CMD} "CREATE DATABASE ${DB} OWNER ${USER};"
${PSQL_CMD} < "${BACKUP_FILE}" >/dev/null
IMPORT_DONE=$(date +%s)
echo "   database import finished in $((IMPORT_DONE - RESTORE_START))s (NOT the RTO — the service is still down)"

echo "== 4/6 verify survived data + working alert flow on the restored database =="
if ! go test -tags restoredrill -count=1 -run TestRestoreDrillVerify ./tests/ -v 2>&1 | grep -E "restore verified|PASS|FAIL|ok"; then
  echo "DRILL FAILED: verification did not pass"
  exit 1
fi

echo "== 5/6 restore the SERVICE: API + worker up, /readiness green =="
BINARY="${WORK_DIR}/nxs-anomaly"
go build -o "${BINARY}" ./cmd/nxs-anomaly
export NXS_ANOMALY_DB_DSN="${NXS_ANOMALY_TEST_DATABASE_URL}"

NXS_ANOMALY_API_KEY="${DRILL_API_KEY}" \
  "${BINARY}" serve --host 127.0.0.1 --port "${API_PORT}" --no-scheduler \
  >"${WORK_DIR}/api.log" 2>&1 &
API_PID=$!
NXS_ANOMALY_WORKER_ADDR="127.0.0.1:${WORKER_PORT}" \
  "${BINARY}" run-worker --poll-interval 2 \
  >"${WORK_DIR}/worker.log" 2>&1 &
WORKER_PID=$!

# The API answers /health only once the store is open and pinging, which is what
# an operator's load balancer waits for.
wait_for "restored API" "http://127.0.0.1:${API_PORT}/health" '"db_ok":true' ||
  { sed -n '1,40p' "${WORK_DIR}/api.log"; exit 1; }

# Service readiness is the product's own answer, not a proxy for it: the
# database check must be ok AND the worker must have written a heartbeat to the
# restored database — an API that serves reads while nothing escalates is not a
# recovered on-call service. (Map keys are marshalled sorted, so "key" is
# immediately followed by "severity".)
wait_for "database check" "http://127.0.0.1:${API_PORT}/api/v1/readiness" \
  '"key":"database","severity":"ok"' "X-API-Key: ${DRILL_API_KEY}" || exit 1
wait_for "worker check" "http://127.0.0.1:${API_PORT}/api/v1/readiness" \
  '"key":"worker","severity":"ok"' "X-API-Key: ${DRILL_API_KEY}" || exit 1

SERVICE_READY=$(date +%s)
RTO=$((SERVICE_READY - RESTORE_START))
echo "   service ready ${RTO}s after the disaster (import $((IMPORT_DONE - RESTORE_START))s + bring-up $((SERVICE_READY - IMPORT_DONE))s)"

echo "== 6/6 rollback compatibility: the previous release on the restored schema =="
# docs/BACKUP_RESTORE.md promises expand/contract migrations, i.e. that the
# previous release keeps working against the new schema — that promise is the
# rollback path, so it is tested rather than asserted. Skipping is possible but
# must be deliberate and is printed loudly.
if [ "${NXS_ANOMALY_DRILL_SKIP_ROLLBACK:-0}" = "1" ]; then
  echo "   !! SKIPPED by NXS_ANOMALY_DRILL_SKIP_ROLLBACK=1 — the rollback path is UNTESTED in this run"
else
  PREV_TAG="${NXS_ANOMALY_DRILL_PREV_TAG:-}"
  if [ -z "${PREV_TAG}" ]; then
    PREV_TAG="$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
      grep -v "^v$(cat VERSION)$" | head -1 || true)"
  fi
  if [ -z "${PREV_TAG}" ]; then
    echo "DRILL FAILED: no previous release tag found (a shallow clone has none — set GIT_DEPTH=0"
    echo "   or pass NXS_ANOMALY_DRILL_PREV_TAG). Not testing the rollback path silently."
    exit 1
  fi
  echo "   previous release: ${PREV_TAG}"
  git worktree add --detach --quiet "${PREV_WORKTREE}" "${PREV_TAG}"
  # Distinguish "the old release cannot be built here" from "the old release
  # cannot run on the new schema" — only the second is a rollback failure.
  if ! (cd "${PREV_WORKTREE}" && go build -o "${WORK_DIR}/nxs-anomaly-prev" ./cmd/nxs-anomaly); then
    echo "DRILL FAILED: could not build ${PREV_TAG} (toolchain/vendor problem, not a rollback verdict)"
    exit 1
  fi

  NXS_ANOMALY_API_KEY="${DRILL_API_KEY}" \
    "${WORK_DIR}/nxs-anomaly-prev" serve --host 127.0.0.1 --port "${PREV_PORT}" --no-scheduler \
    >"${WORK_DIR}/prev.log" 2>&1 &
  PREV_PID=$!
  wait_for "${PREV_TAG} API on the new schema" "http://127.0.0.1:${PREV_PORT}/health" '"db_ok":true' ||
    { sed -n '1,40p' "${WORK_DIR}/prev.log"; exit 1; }

  # Reading is not enough: a rollback has to keep taking alerts. Ingest through
  # the OLD binary into the NEW schema.
  CANARY_KEY="$(${PSQL_CMD} -tAc "SELECT key FROM nxs_anomaly_integrations ORDER BY created_at LIMIT 1" | tr -d '[:space:]')"
  if [ -z "${CANARY_KEY}" ]; then
    echo "DRILL FAILED: no integration key to ingest with"
    exit 1
  fi
  if ! curl -fsS -X POST -H 'Content-Type: application/json' \
    -d '{"title":"rollback-drill alert"}' \
    "http://127.0.0.1:${PREV_PORT}/integrations/v1/webhook/${CANARY_KEY}" >/dev/null; then
    echo "DRILL FAILED: ${PREV_TAG} could not ingest against the restored schema —"
    echo "   the documented rollback path does not work. Check the expand/contract rule"
    echo "   in docs/BACKUP_RESTORE.md against the migrations added since ${PREV_TAG}."
    exit 1
  fi
  kill "${PREV_PID}" >/dev/null 2>&1 || true
  PREV_PID=""
  echo "   ${PREV_TAG} serves and ingests against the restored schema — rollback path works"
fi

echo
echo "== restore drill PASSED =="
echo "   RPO (recovery point): the ${RPO_AT} backup; bounded in production by backup cadence (target <= 5 min, see docs/BACKUP_RESTORE.md)"
echo "   RTO (disaster -> service readiness): ${RTO}s (budget ${RTO_BUDGET_SECONDS}s)"
if [ "${RTO}" -gt "${RTO_BUDGET_SECONDS}" ]; then
  echo "DRILL FAILED: RTO ${RTO}s exceeds budget ${RTO_BUDGET_SECONDS}s"
  exit 1
fi
