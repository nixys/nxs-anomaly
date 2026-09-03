#!/usr/bin/env bash
# Point-in-time recovery drill for nxs-anomaly (BETA-041, DR acceptance).
#
# tests/restore_drill.sh proves a *logical* restore: the last dump comes back and
# the service returns. That bounds RPO by the dump cadence — hours, not the five
# minutes docs/BACKUP_RESTORE.md promises. The five-minute figure rests on WAL
# archiving, and this drill is what proves the WAL side actually works:
#
#   archive WAL -> base backup -> write, mark a target time, write again ->
#   destroy the cluster -> restore the base backup and replay WAL to the target ->
#   assert the pre-target write is back and the post-target write is gone.
#
# "The post-target write is gone" is the assertion that matters. A recovery that
# replays everything looks identical to a successful PITR on a green run, and
# would silently be unable to rewind past an accidental DELETE — the case PITR
# exists for.
#
# Needs a Docker daemon: this drives PostgreSQL configuration and the data
# directory, which a CI `services:` database cannot expose. Managed PostgreSQL
# offers the same capability behind its own console/API — the runbook step this
# drill validates the mechanics of is in docs/BACKUP_RESTORE.md.
#
# The archive and the base backup live in a named docker volume rather than a
# bind mount, and the database host is configurable, so the drill runs unchanged
# against docker-in-docker in CI (where the daemon's filesystem is not the job's,
# and published ports appear on the dind host, not on localhost).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

IMAGE="${NXS_ANOMALY_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
PORT="${NXS_ANOMALY_TEST_POSTGRES_PORT:-55449}"
DB="${NXS_ANOMALY_TEST_POSTGRES_DB:-nxs_anomaly_pitr}"
USER="${NXS_ANOMALY_TEST_POSTGRES_USER:-nxs_anomaly}"
PASSWORD="${NXS_ANOMALY_TEST_POSTGRES_PASSWORD:-nxs_anomaly}"
PRIMARY="nxs-anomaly-pitr-primary-$$"
RESTORED="nxs-anomaly-pitr-restored-$$"
DATA_VOLUME="nxs-anomaly-pitr-data-$$"
PITR_VOLUME="nxs-anomaly-pitr-archive-$$"
PGHOST_FOR_CLIENTS="${NXS_ANOMALY_TEST_POSTGRES_HOST:-127.0.0.1}"
RTO_BUDGET_SECONDS="${NXS_ANOMALY_PITR_RTO_BUDGET:-1800}"
READY_TIMEOUT="${NXS_ANOMALY_PITR_READY_TIMEOUT:-120}"

cleanup() {
  if [ "${NXS_ANOMALY_KEEP_TEST_POSTGRES:-0}" != "1" ]; then
    docker rm -f "${PRIMARY}" "${RESTORED}" >/dev/null 2>&1 || true
    docker volume rm -f "${DATA_VOLUME}" "${PITR_VOLUME}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# The archive volume is shared by the primary (which writes) and the restore
# (which reads). World-writable inside: postgres runs as uid 70 in the alpine
# image, and the restore helper runs as root.
docker volume create "${PITR_VOLUME}" >/dev/null
docker run --rm -v "${PITR_VOLUME}:/pitr" "${IMAGE}" \
  sh -c 'mkdir -p /pitr/wal /pitr/backup && chmod -R 777 /pitr' >/dev/null

# Inspect the archive volume from a throwaway container: the host cannot see it.
in_archive() { docker run --rm -v "${PITR_VOLUME}:/pitr" "${IMAGE}" sh -c "$1"; }

wait_ready() {
  local container="$1" deadline
  deadline=$(( $(date +%s) + READY_TIMEOUT ))
  while [ "$(date +%s)" -lt "${deadline}" ]; do
    # Over TCP, not the socket: during startup the entrypoint runs a temporary
    # server that listens on the unix socket only, and during recovery postgres
    # refuses connections until it has promoted.
    if docker exec "${container}" pg_isready -h 127.0.0.1 -U "${USER}" -d "${DB}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "DRILL FAILED: ${container} did not accept connections within ${READY_TIMEOUT}s"
  docker logs --tail 40 "${container}" 2>&1 || true
  return 1
}

psql_primary() { docker exec -i "${PRIMARY}" psql -v ON_ERROR_STOP=1 -U "${USER}" -d "${DB}" "$@"; }
psql_restored() { docker exec -i "${RESTORED}" psql -v ON_ERROR_STOP=1 -U "${USER}" -d "${DB}" "$@"; }

echo "== 1/6 start a primary with WAL archiving on =="
docker run --name "${PRIMARY}" \
  -e "POSTGRES_DB=${DB}" -e "POSTGRES_USER=${USER}" -e "POSTGRES_PASSWORD=${PASSWORD}" \
  -v "${PITR_VOLUME}:/pitr" -p "${PORT}:5432" -d "${IMAGE}" \
  postgres -c wal_level=replica -c archive_mode=on \
  -c "archive_command=test ! -f /pitr/wal/%f && cp %p /pitr/wal/%f" \
  -c archive_timeout=30 >/dev/null
wait_ready "${PRIMARY}"

echo "== 2/6 seed the application schema and a base backup =="
# The real schema, created by the application's own migrations, so the drill
# restores what production actually stores.
export NXS_ANOMALY_TEST_DATABASE_URL="postgres://${USER}:${PASSWORD}@${PGHOST_FOR_CLIENTS}:${PORT}/${DB}?sslmode=disable"
export GOCACHE="${GOCACHE:-/tmp/go-build}"
export GOMODCACHE="${GOMODCACHE:-/tmp/go-mod}"
export GOFLAGS="${GOFLAGS:--mod=vendor}"
go test -tags restoredrill -count=1 -run TestRestoreDrillSeed ./tests/ >/dev/null 2>&1 ||
  { echo "DRILL FAILED: could not seed the schema"; exit 1; }

docker exec "${PRIMARY}" pg_basebackup -U "${USER}" -D /pitr/backup -Ft -z -Xs >/dev/null
echo "   base backup: $(in_archive 'du -sh /pitr/backup | cut -f1')"

echo "== 3/6 write, mark a recovery target, write again =="
psql_primary -q -c "CREATE TABLE pitr_drill_marks (id text primary key, at timestamptz not null default now())"
psql_primary -q -c "INSERT INTO pitr_drill_marks (id) VALUES ('before-target')"
# A recovery target between the two writes. Postgres resolves the target against
# commit timestamps, so a second of slack either side keeps the drill immune to
# clock granularity rather than flaky about it.
sleep 2
TARGET_TIME="$(psql_primary -tAc "SELECT now()")"
sleep 2
psql_primary -q -c "INSERT INTO pitr_drill_marks (id) VALUES ('after-target')"
# Close and archive the current segment so the target is reachable from the
# archive alone — production relies on archive_timeout for the same reason.
psql_primary -q -c "SELECT pg_switch_wal()" >/dev/null
sleep 3
echo "   recovery target: ${TARGET_TIME}; archived segments: $(in_archive 'find /pitr/wal -type f | wc -l' | tr -d "[:space:]")"

echo "== 4/6 DISASTER: destroy the cluster =="
RESTORE_START=$(date +%s)
docker rm -f "${PRIMARY}" >/dev/null
echo "   primary destroyed (its data directory is gone with it)"

echo "== 5/6 restore the base backup and replay WAL to the target =="
docker volume create "${DATA_VOLUME}" >/dev/null
docker run --rm -v "${DATA_VOLUME}:/data" -v "${PITR_VOLUME}:/pitr" "${IMAGE}" sh -c "
  set -e
  tar xzf /pitr/backup/base.tar.gz -C /data
  tar xzf /pitr/backup/pg_wal.tar.gz -C /data/pg_wal
  # recovery.signal puts the cluster in targeted recovery; without
  # recovery_target_action=promote it would stop and wait, which in a drill is
  # indistinguishable from a hang.
  touch /data/recovery.signal
  cat >> /data/postgresql.auto.conf <<CONF
restore_command = 'cp /pitr/wal/%f %p'
recovery_target_time = '${TARGET_TIME}'
recovery_target_action = 'promote'
CONF
  chown -R postgres:postgres /data
  chmod 700 /data
" >/dev/null

docker run --name "${RESTORED}" \
  -e "POSTGRES_PASSWORD=${PASSWORD}" -e "POSTGRES_USER=${USER}" -e "POSTGRES_DB=${DB}" \
  -v "${DATA_VOLUME}:/var/lib/postgresql/data" -v "${PITR_VOLUME}:/pitr" \
  -d "${IMAGE}" >/dev/null
wait_ready "${RESTORED}"

# Accepting connections is not recovery: with hot_standby on, a cluster still
# replaying WAL answers pg_isready and serves reads, and every write fails with
# "read-only transaction". The recovery is over when the cluster has promoted,
# so that — not connectivity — is what stops the clock.
promoted=0
deadline=$(( $(date +%s) + READY_TIMEOUT ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  if [ "$(psql_restored -tAc 'SELECT pg_is_in_recovery()' 2>/dev/null | tr -d '[:space:]')" = "f" ]; then
    promoted=1
    break
  fi
  sleep 1
done
if [ "${promoted}" != "1" ]; then
  echo "DRILL FAILED: the restored cluster never promoted — it is still replaying WAL, so the"
  echo "   service would come back read-only. Check recovery_target_action and the archive."
  docker logs --tail 40 "${RESTORED}" 2>&1 || true
  exit 1
fi
RESTORE_END=$(date +%s)
RTO=$((RESTORE_END - RESTORE_START))

echo "== 6/6 verify the recovery landed on the target, not on the end of the WAL =="
BEFORE="$(psql_restored -tAc "SELECT count(*) FROM pitr_drill_marks WHERE id='before-target'" | tr -d '[:space:]')"
AFTER="$(psql_restored -tAc "SELECT count(*) FROM pitr_drill_marks WHERE id='after-target'" | tr -d '[:space:]')"
CANARY="$(psql_restored -tAc "SELECT count(*) FROM nxs_anomaly_integrations" | tr -d '[:space:]')"

if [ "${BEFORE}" != "1" ]; then
  echo "DRILL FAILED: the pre-target write did not survive (found ${BEFORE}) — WAL replay lost committed data"
  exit 1
fi
if [ "${AFTER}" != "0" ]; then
  echo "DRILL FAILED: the post-target write is still present — recovery replayed past"
  echo "   recovery_target_time='${TARGET_TIME}'. The cluster cannot be rewound to a"
  echo "   point in time, so an accidental delete could not be undone."
  exit 1
fi
if [ "${CANARY}" -lt 1 ]; then
  echo "DRILL FAILED: application data from the base backup is missing (integrations=${CANARY})"
  exit 1
fi
# It has to be a working database afterwards, not just a readable one.
psql_restored -q -c "INSERT INTO pitr_drill_marks (id) VALUES ('post-recovery-write')"

echo
echo "== PITR drill PASSED =="
echo "   recovered to ${TARGET_TIME}: pre-target write present, post-target write correctly absent,"
echo "   application schema and data intact, cluster writable after promotion"
echo "   RPO: bounded by archive_timeout (30s here; production target <= 5 min, see docs/BACKUP_RESTORE.md)"
echo "   RTO (disaster -> promoted, writable database): ${RTO}s (budget ${RTO_BUDGET_SECONDS}s)"
if [ "${RTO}" -gt "${RTO_BUDGET_SECONDS}" ]; then
  echo "DRILL FAILED: RTO ${RTO}s exceeds budget ${RTO_BUDGET_SECONDS}s"
  exit 1
fi
