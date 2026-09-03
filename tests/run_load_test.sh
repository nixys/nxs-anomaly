#!/usr/bin/env bash
# BETA-022 capacity/chaos runner. Provisions a throwaway PostgreSQL (unless one
# is supplied via NXS_ANOMALY_TEST_DATABASE_URL) and runs the load harness across
# the fixed profiles and chaos scenarios. Non-zero exit if any profile fails its
# acceptance criteria (see tests/loadtest/loadtest.go).
#
# Env knobs:
#   NXS_ANOMALY_TEST_DATABASE_URL  use an existing DB instead of spinning one up
#   LOADTEST_ALERTS  (default 1500)  alerts per profile
#   LOADTEST_RATE    (default 150)   ingest rate, alerts/sec
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOMODCACHE="${GOMODCACHE:-/tmp/go-mod}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"

# The harness opens its own store per profile, and a PostgreSQL that is still
# settling answers with a connection reset rather than a refusal. Without a
# budget the profile dies at store init with no report at all — which is what
# four baseline profiles did on a loaded machine, looking like a product failure
# and not like the environment it was. Same knob the deployments use.
export NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS="${NXS_ANOMALY_DB_CONNECT_MAX_WAIT_SECONDS:-60}"

ALERTS="${LOADTEST_ALERTS:-1500}"
RATE="${LOADTEST_RATE:-150}"

run_profiles() {
  # profile: "<workers> <recipients> <scenario>"
  # profile: "<workers> <recipients> <scenario> <integrations> <drain-timeout>"
  #
  # The last column is the axis the suite used to lack. Ingest serialises per
  # integration and its group lookup is scoped to one, so every profile below
  # measured the same single hot path; a deployment fed by several sources
  # behaves differently and nothing covered it.
  #
  # The drain budget is per profile, not one constant, because it is a property
  # of the scenario's delivery cost rather than of the suite. provider_timeout
  # holds ~20% of 4500 deliveries behind a 2s provider; it used to fit inside 60s
  # only because ingest was slow enough to spread the work out. Once ingest got
  # ~3x faster the same run delivered 1335/1500 inside 60s and 1500/1500 inside
  # 180s — nothing was lost, the backlog simply had less time to drain. A budget
  # that silently depends on the producer being slow is not an acceptance
  # criterion, it is a coincidence.
  #
  # worker_kill carries a budget of its own for the same reason from the other
  # side: what the killed worker stranded in 'delivering' only comes back when
  # the reaper's lease expires, so the budget has to outlast that lease (the
  # harness shortens it for this scenario alone) plus the redelivery it starts.
  local profiles=(
    "1 3 baseline 1 60s"
    "2 3 baseline 1 60s"
    "4 3 baseline 1 60s"
    "2 3 baseline 8 60s"
    "2 3 provider_timeout 1 180s"
    "2 3 worker_kill 1 90s"
    "2 3 retry_storm 1 90s"
  )
  local failed=0
  for p in "${profiles[@]}"; do
    read -r workers recipients scenario integrations drain <<<"${p}"
    echo "=== profile: workers=${workers} recipients=${recipients} scenario=${scenario} integrations=${integrations} drain=${drain} alerts=${ALERTS} rate=${RATE} ==="
    if ! go run -tags loadtest ./tests/loadtest \
        -alerts "${ALERTS}" -rate "${RATE}" -integrations "${integrations}" -drain-timeout "${drain}" \
        -workers "${workers}" -recipients "${recipients}" -scenario "${scenario}"; then
      echo "!!! profile FAILED: ${scenario} (workers=${workers})"
      failed=1
    fi
  done
  return "${failed}"
}

if [ -n "${NXS_ANOMALY_TEST_DATABASE_URL:-}" ]; then
  export NXS_ANOMALY_DB_DSN="${NXS_ANOMALY_TEST_DATABASE_URL}"
  run_profiles
  exit $?
fi

IMAGE="${NXS_ANOMALY_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
PORT="${NXS_ANOMALY_TEST_POSTGRES_PORT:-55460}"
DB="${NXS_ANOMALY_TEST_POSTGRES_DB:-nxs_anomaly_load}"
USER="${NXS_ANOMALY_TEST_POSTGRES_USER:-nxs_anomaly}"
PASSWORD="${NXS_ANOMALY_TEST_POSTGRES_PASSWORD:-nxs_anomaly}"
CONTAINER="${NXS_ANOMALY_TEST_POSTGRES_CONTAINER:-nxs-anomaly-load-postgres-$$}"

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

for _ in $(seq 1 60); do
  if docker exec "${CONTAINER}" pg_isready -U "${USER}" -d "${DB}" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${CONTAINER}" pg_isready -U "${USER}" -d "${DB}" >/dev/null

export NXS_ANOMALY_DB_DSN="postgres://${USER}:${PASSWORD}@127.0.0.1:${PORT}/${DB}?sslmode=disable"
export NXS_ANOMALY_TEST_DATABASE_URL="${NXS_ANOMALY_DB_DSN}"

run_profiles
