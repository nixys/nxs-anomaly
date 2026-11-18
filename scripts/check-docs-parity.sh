#!/usr/bin/env bash
# Every published edition must provide the same documents in both languages.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
python3 - <<'PY'
from pathlib import Path
failed = False
for edition in ('community', 'enterprise'):
    root = Path('docs') / edition
    if not root.exists():
        continue  # Enterprise is intentionally absent in the GitHub CE tree.
    docs = {lang: {p.name for p in (root / lang).glob('*.md')} for lang in ('en', 'ru')}
    for lang, other in [('en', 'ru'), ('ru', 'en')]:
        if not docs[lang]:
            print(f'FAIL: {root / lang} is empty')
            failed = True
        for missing in sorted(docs[other] - docs[lang]):
            print(f'FAIL: missing translation {root / lang / missing}')
            failed = True
    print(f'{edition}: EN={len(docs["en"])} RU={len(docs["ru"])}')
raise SystemExit(1 if failed else 0)
PY
