#!/usr/bin/env bash
# make-community-docs.sh — produce docs/community/ru from docs/enterprise/ru.
#
# The Russian community set is derived, not written twice. Four directories of
# largely identical prose is exactly the shape that drifts: a correction lands in
# one and not the other, and nobody notices until a reader hits the stale half.
# Here the enterprise Russian set is the source, the enterprise-only material is
# marked in it, and this script cuts along those marks.
#
# The English sets are NOT derived — a marker can cut text but cannot translate
# it, so they are written and kept honest by scripts/check-docs-parity.sh.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${ROOT_DIR}/docs/enterprise/ru"
DST="${1:-${ROOT_DIR}/docs/community/ru}"

# Documents that exist only for the enterprise edition.
ENTERPRISE_ONLY=(KAFKA.md INCIDENT_ANALYTICS_DASHBOARDS.md)

rm -rf "${DST}"; mkdir -p "${DST}"
for src in "${SRC}"/*.md; do
  name="$(basename "$src")"
  skip=0
  for e in "${ENTERPRISE_ONLY[@]}"; do [ "$name" = "$e" ] && skip=1; done
  [ "$skip" = 1 ] && continue
  python3 - "$src" "${DST}/${name}" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
BEGIN, END = 'nxs:enterprise:begin', 'nxs:enterprise:end'
out, skip = [], False
for line in open(src, encoding='utf-8').read().split('\n'):
    token = line.strip()
    for opener in ('<!--', '#'):
        if token.startswith(opener):
            token = token[len(opener):]
            break
    for closer in ('-->',):
        if token.endswith(closer):
            token = token[: -len(closer)]
    token = token.strip()
    if token == BEGIN:
        if skip:
            sys.exit(f'nested marker in {src}')
        skip = True
        continue
    if token == END:
        skip = False
        continue
    if not skip:
        out.append(line)
if skip:
    sys.exit(f'unterminated marker in {src}')
# Two blank lines left where a block was cut read as a formatting slip.
text = '\n'.join(out)
while '\n\n\n' in text:
    text = text.replace('\n\n\n', '\n\n')
open(dst, 'w', encoding='utf-8').write(text)
PY
done
# Community-specific installation and contributor instructions cannot inherit
# private registries or CI commands. Keep their authored overrides in the overlay.
if [ -d "${ROOT_DIR}/packaging/community/docs/community/ru" ]; then
  cp -a "${ROOT_DIR}/packaging/community/docs/community/ru/." "${DST}/"
fi
# Both languages are published side by side; point each document at the other.
python3 "${ROOT_DIR}/scripts/add-doc-language-switch.py" "${DST}" "${ROOT_DIR}/docs/community/en" "In English"

echo "  docs/community/ru: $(ls "${DST}" | wc -l) документов, $(cat "${DST}"/*.md | wc -l) строк"
