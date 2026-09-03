#!/usr/bin/env sh
# Start the nxs-anomaly API for the Playwright e2e run. Playwright launches this
# from the frontend/ directory (where playwright.config.ts lives) and waits for
# /health to go green before running specs.
#
# Requires a reachable PostgreSQL in NXS_ANOMALY_TEST_DATABASE_URL. The scheduler
# stays on with a 1s poll so an ingested alert escalates and delivers (through the
# log channel — no external provider) within the test's wait window.
set -eu

: "${NXS_ANOMALY_TEST_DATABASE_URL:?set NXS_ANOMALY_TEST_DATABASE_URL to a reachable PostgreSQL}"

cd "$(dirname "$0")/../.." # repo root

export NXS_ANOMALY_DB_DSN="$NXS_ANOMALY_TEST_DATABASE_URL"
export NXS_ANOMALY_BOOTSTRAP_ADMIN_USERNAME="${E2E_ADMIN_USER:-e2e-admin}"
export NXS_ANOMALY_BOOTSTRAP_ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-e2e-password-1234}"
export NXS_ANOMALY_API_KEYS="${E2E_API_KEY:-e2e-key}:admin"
# The e2e talks plain HTTP on localhost, so the session cookie must not be Secure.
export NXS_ANOMALY_SESSION_COOKIE_SECURE=false
export NXS_ANOMALY_POLL_INTERVAL=1
# Make a failing delivery reach the terminal "failed" state on the first attempt,
# so the delivery-failure e2e is deterministic and fast (no real retry backoff).
export NXS_ANOMALY_NOTIFICATION_MAX_RETRIES=1
# Enable team scoping so the RBAC/cross-team spec can prove isolation. Admins
# (every other spec signs in as the bootstrap admin) are exempt, so this is inert
# for them.
export NXS_ANOMALY_TEAM_SCOPING=true
export GOFLAGS="${GOFLAGS:--mod=vendor}"

# Use a prebuilt binary when one is provided (e.g. running inside the Playwright
# container image, which has the browser system libraries but no Go toolchain).
# Otherwise compile on the fly, which is the convenient path on a dev machine.
if [ -n "${NXS_ANOMALY_E2E_BINARY:-}" ]; then
  exec "$NXS_ANOMALY_E2E_BINARY" serve --host 127.0.0.1 --port "${E2E_API_PORT:-8080}"
fi
exec go run ./cmd/nxs-anomaly serve --host 127.0.0.1 --port "${E2E_API_PORT:-8080}"
