#!/usr/bin/env bash
# check-docs-parity.sh — report which documents exist in which edition and
# language, and fail on the pairs that must not diverge.
#
# docs/ carries four sets: two editions × two languages. Three relationships
# hold between them, and only two can be checked mechanically:
#
#   enterprise/ru → community/ru   derived by scripts/make-community-docs.sh.
#                                  Checked: regenerate and diff, the same way CI
#                                  diffs the chart's alert rules against their
#                                  source. A hand edit to community/ru is a bug,
#                                  because the next generation erases it.
#
#   */ru → */en                    translation. A marker can cut text; it cannot
#                                  translate it, so the English sets are written,
#                                  not derived. Nothing here can judge a
#                                  translation — what it can do is name the
#                                  documents that have no English counterpart
#                                  yet, so the gap is a number somebody watches
#                                  rather than a surprise at publication.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"
fail=0

# ── 1. community/ru must be exactly what the generator produces ──────────────
tmp="$(mktemp -d)"
bash scripts/make-community-docs.sh "${tmp}" >/dev/null
if diff -rq "${tmp}" docs/community/ru >/dev/null 2>&1; then
  echo "ok: docs/community/ru matches marked shared sources plus Community overrides"
else
  echo "FAIL: docs/community/ru is not what the markers produce."
  echo "      Edit docs/enterprise/ru or packaging/community/docs/community/ru, then regenerate:"
  echo "        bash scripts/make-community-docs.sh"
  diff -rq "${tmp}" docs/community/ru | sed 's/^/    /' | head -20
  fail=1
fi
rm -rf "${tmp}"

# ── 2. English coverage, reported ────────────────────────────────────────────
# Only the community edition is published in English. The enterprise edition is
# delivered in Russian, so docs/enterprise/en does not exist and is not expected
# to: a directory nobody fills is a permanent red mark that teaches people to
# ignore the gate.
missing=0
for ru in docs/community/ru/*.md; do
  [ -e "$ru" ] || continue
  en="docs/community/en/$(basename "$ru")"
  [ -f "$en" ] || { echo "    missing: ${en}"; missing=$((missing + 1)); }
done
if [ "${missing}" = 0 ]; then
  echo "ok: every document has an English counterpart"
else
  echo "note: ${missing} document(s) not translated yet (listed above)"
fi

# ── 3. The published set must not be empty ───────────────────────────────────
# The community edition is published in English. An empty set would ship a
# repository whose docs/ has no prose, which the generator also refuses.
if [ -z "$(find docs/community/en -name '*.md' -print -quit 2>/dev/null)" ]; then
  echo "FAIL: docs/community/en is empty — the published tree would carry no documentation"
  fail=1
else
  echo "ok: docs/community/en carries $(find docs/community/en -name '*.md' | wc -l) document(s)"
fi

[ "${fail}" = 0 ] || exit 1
