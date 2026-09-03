#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

IMAGE="${NXS_ANOMALY_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
PORT="${NXS_ANOMALY_TEST_POSTGRES_PORT:-55432}"
DB="${NXS_ANOMALY_TEST_POSTGRES_DB:-nxs_anomaly_test}"
USER="${NXS_ANOMALY_TEST_POSTGRES_USER:-nxs_anomaly}"
PASSWORD="${NXS_ANOMALY_TEST_POSTGRES_PASSWORD:-nxs_anomaly}"
CONTAINER="${NXS_ANOMALY_TEST_POSTGRES_CONTAINER:-nxs-anomaly-test-postgres-$$}"

cd "${ROOT_DIR}"

export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOMODCACHE="${GOMODCACHE:-/tmp/go-mod}"

# When a database is already provided (CI runs it as a GitLab service, where
# Docker is not available), use it as-is and only run the suite.
if [ -n "${NXS_ANOMALY_TEST_DATABASE_URL:-}" ]; then
  export NXS_ANOMALY_DB_DSN="${NXS_ANOMALY_TEST_DATABASE_URL}"
  # GitLab starts the job container before its PostgreSQL service is guaranteed
  # to accept connections. The store retries transient startup failures for this
  # budget; if the service never starts, integration tests fail instead of
  # silently skipping and producing a misleading unit-only coverage profile.
  export NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS="${NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS:-30}"
  # Unquoted on purpose: see the note at the bottom of this script.
  exec go test ${NXS_ANOMALY_TEST_GO_TEST_ARGS:-} ./...
fi

cleanup() {
  if [ "${NXS_ANOMALY_KEEP_TEST_POSTGRES:-0}" != "1" ]; then
    docker stop "${CONTAINER}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

docker run \
  --name "${CONTAINER}" \
  -e "POSTGRES_DB=${DB}" \
  -e "POSTGRES_USER=${USER}" \
  -e "POSTGRES_PASSWORD=${PASSWORD}" \
  -p "127.0.0.1:${PORT}:5432" \
  -d "${IMAGE}" >/dev/null

# Two consecutive successes, because one is not enough: PostgreSQL answers
# pg_isready during startup and can still refuse the very next connection while
# it finishes recovery. The old shape — a loop, then one bare check outside it —
# turned that flap into `set -e` killing the script before `go test` ran, with an
# empty log and a non-zero exit that looks like the suite failed rather than
# never having started.
ready=0
for _ in $(seq 1 60); do
  if docker exec "${CONTAINER}" pg_isready -U "${USER}" -d "${DB}" >/dev/null 2>&1; then
    ready=$((ready + 1))
    [ "${ready}" -ge 2 ] && break
  else
    ready=0
  fi
  sleep 1
done

if [ "${ready}" -lt 2 ]; then
  echo "PostgreSQL in ${CONTAINER} did not become ready" >&2
  exit 1
fi

export NXS_ANOMALY_DB_DSN="postgres://${USER}:${PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
export NXS_ANOMALY_TEST_DATABASE_URL="${NXS_ANOMALY_DB_DSN}"

# Extra `go test` flags (e.g. coverage) can be injected via env. Unquoted on
# purpose so multiple space-separated flags split into separate arguments.
go test ${NXS_ANOMALY_TEST_GO_TEST_ARGS:-} ./...
