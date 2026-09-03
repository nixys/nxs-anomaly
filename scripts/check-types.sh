#!/usr/bin/env sh
# check-types.sh — TypeScript type check for the frontend.
#
# Why this runs in pre-commit: `npm run build` in the test:frontend CI job runs
# `tsc -b --noEmit` first, so a type error is already a hard gate — but it is
# found a full pipeline later, on a commit that is already pushed. The same
# check costs a couple of seconds locally on an incremental build.
#
# It checks the working tree, not the index — the same compromise check-docs.sh
# makes: tsc needs a real project on disk, and a partially staged frontend is
# rare enough not to justify a temporary checkout.
#
# Usage: scripts/check-types.sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Nothing frontend-shaped in the commit: a backend-only commit should not pay
# for a tsc run. tsconfig/package.json count too — they change what compiles.
if ! git -C "${ROOT_DIR}" diff --cached --name-only --diff-filter=ACMR \
	| grep -qE '^frontend/.*\.(ts|tsx)$|^frontend/(tsconfig\.json|package\.json)$'; then
	exit 0
fi

if [ ! -d "${ROOT_DIR}/frontend/node_modules" ]; then
	echo "check-types: frontend/node_modules is missing; skipping the type check." >&2
	echo "check-types: run 'npm ci' in frontend/ to get it locally (CI gates it either way)." >&2
	exit 0
fi

cd "${ROOT_DIR}/frontend"
npm run --silent typecheck
