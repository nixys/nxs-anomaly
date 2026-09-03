#!/usr/bin/env python3
"""Insert the language switch into a community document set.

Both community languages are published side by side, so every document points at
its counterpart. The line is inserted by the tooling rather than kept in the
source for two reasons: the enterprise set has no counterpart to point at, and a
link is written only when the other file actually exists — a switch that leads
nowhere is worse than none, because the reader clicks it.

    add-doc-language-switch.py <dir to annotate> <counterpart dir> <label>
"""
import os
import sys

target, other, label = sys.argv[1], sys.argv[2], sys.argv[3]
rel = os.path.basename(other)
added = 0

for name in sorted(os.listdir(target)):
    if not name.endswith('.md'):
        continue
    if not os.path.exists(os.path.join(other, name)):
        continue
    path = os.path.join(target, name)
    lines = open(path, encoding='utf-8').read().split('\n')
    link = f'*{label}: [{name}](../{rel}/{name})*'
    if link in lines:
        continue
    # Directly under the H1, where somebody looking for their language looks.
    for i, line in enumerate(lines):
        if line.startswith('# '):
            lines[i + 1:i + 1] = ['', link]
            added += 1
            break
    open(path, 'w', encoding='utf-8').write('\n'.join(lines))

print(f'  language switch: {added} document(s) in {target}')
