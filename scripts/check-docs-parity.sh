#!/usr/bin/env bash
# check-docs-parity.sh — report which shared documents the community edition
# carries in English, and fail if it carries none.
#
# docs/ carries two sets: the enterprise edition in Russian, which is the source
# of truth for prose, and the community edition in English, translated from it.
# A translation cannot be derived or judged mechanically — what can be done is
# name the shared documents that have no English counterpart yet, so the gap is
# a number somebody watches rather than a surprise at publication.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"
fail=0

# Documents that exist only for the enterprise edition.
ENTERPRISE_ONLY=(KAFKA.md INCIDENT_ANALYTICS_DASHBOARDS.md HA.md)

# ── 1. English coverage, reported ────────────────────────────────────────────
missing=0
for ru in docs/enterprise/ru/*.md; do
  [ -e "$ru" ] || continue
  name="$(basename "$ru")"
  skip=0
  for e in "${ENTERPRISE_ONLY[@]}"; do [ "$name" = "$e" ] && skip=1; done
  [ "$skip" = 1 ] && continue
  en="docs/community/en/${name}"
  [ -f "$en" ] || { echo "    missing: ${en}"; missing=$((missing + 1)); }
done
if [ "${missing}" = 0 ]; then
  echo "ok: every shared document has an English counterpart"
else
  echo "note: ${missing} document(s) not translated yet (listed above)"
fi

# ── 2. The published set must not be empty ───────────────────────────────────
# The community edition is published in English only. An empty set would ship a
# repository whose docs/ has no prose, which the generator also refuses.
if [ -z "$(find docs/community/en -name '*.md' -print -quit 2>/dev/null)" ]; then
  echo "FAIL: docs/community/en is empty — the published tree would carry no documentation"
  fail=1
else
  echo "ok: docs/community/en carries $(find docs/community/en -name '*.md' | wc -l) document(s)"
fi

[ "${fail}" = 0 ] || exit 1
