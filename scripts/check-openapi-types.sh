#!/usr/bin/env sh
# check-openapi-types.sh — frontend/src/api/schema.d.ts must match docs/openapi.json.
#
# Why this runs in pre-commit: schema.d.ts is generated from docs/openapi.json by
# `npm run openapi:types`, and the test:frontend:openapi CI job fails the pipeline
# when the two drift apart. Editing the spec without regenerating is easy to do and
# costs a full pipeline to find out about — see v0.1.69, where the openapi.json
# change for the ingest pipeline field shipped without the regenerated types.
#
# Unlike the CI job this does not only report the drift: it regenerates the file in
# place, so the fix is `git add`, not a second command. The commit still fails —
# staging a file the author has not seen would be worse than stopping.
#
# It regenerates from the working tree, not the index, the same compromise
# check-types.sh makes.
#
# Usage: scripts/check-openapi-types.sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Same inputs the test:frontend:openapi job watches: the spec, the generated file,
# and package.json — the generator version lives there.
if ! git -C "${ROOT_DIR}" diff --cached --name-only --diff-filter=ACMR \
	| grep -qE '^docs/openapi\.json$|^frontend/src/api/schema\.d\.ts$|^frontend/package\.json$'; then
	exit 0
fi

if [ ! -d "${ROOT_DIR}/frontend/node_modules" ]; then
	echo "check-openapi-types: frontend/node_modules is missing; skipping the check." >&2
	echo "check-openapi-types: run 'npm ci' in frontend/ to get it locally (CI gates it either way)." >&2
	exit 0
fi

SCHEMA="frontend/src/api/schema.d.ts"
before="$(git -C "${ROOT_DIR}" hash-object "${ROOT_DIR}/${SCHEMA}")"

cd "${ROOT_DIR}/frontend"
npm run --silent openapi:types

after="$(git -C "${ROOT_DIR}" hash-object "${ROOT_DIR}/${SCHEMA}")"
if [ "${before}" != "${after}" ]; then
	echo "check-openapi-types: ${SCHEMA} was stale; it has been regenerated." >&2
	echo "check-openapi-types: review the diff and 'git add ${SCHEMA}', then commit again." >&2
	exit 1
fi
