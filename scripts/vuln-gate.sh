#!/usr/bin/env bash
# vuln-gate.sh — supply-chain gate over `govulncheck -json`, the Go-side
# counterpart of frontend/scripts/audit-gate.mjs.
#
# Why not raw `govulncheck ./...`? Because it is binary: any finding fails, with
# no way to say "we looked at this one, it does not apply, re-check in a month".
# The frontend has had that mechanism since the beta gate and the Go side has
# not, so the only options here were "fix immediately" or "delete the job".
# Neither is what you want at 05:00 when a transitive dependency of a dependency
# publishes an advisory in a code path you never call.
#
# This gate fails on every finding EXCEPT ones listed in .vuln-allowlist.json
# with a reason and a reviewBy date, and it fails if any entry is past its
# reviewBy — so an exception cannot rot into a permanent silence.
#
# It gates on govulncheck's *symbol* findings ("your code calls it"), not on the
# module-level ones ("something in your dependency tree contains it"). The
# module-level set is reported for visibility. That is the same distinction
# govulncheck makes itself, and gating on the wider set would mean failing on
# code that provably cannot run here.
#
# Usage: scripts/vuln-gate.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

ALLOWLIST="${NXS_ANOMALY_VULN_ALLOWLIST:-.vuln-allowlist.json}"
GOVULNCHECK="${GOVULNCHECK:-govulncheck}"
OUT="$(mktemp)"
trap 'rm -f "${OUT}"' EXIT

# govulncheck exits non-zero when it finds something; we want the JSON either
# way, and a real tool failure is caught by the empty-output check below.
"${GOVULNCHECK}" -json ./... >"${OUT}" 2>/dev/null || true
if [ ! -s "${OUT}" ]; then
  echo "vuln-gate: govulncheck produced no output — the scan itself failed"
  exit 2
fi

python3 - "${OUT}" "${ALLOWLIST}" <<'PY'
import json, sys, datetime

stream_path, allow_path = sys.argv[1], sys.argv[2]

# govulncheck -json is a stream of single-key JSON objects. Findings carry an
# OSV id and a trace; a finding whose innermost frame names a function is a
# symbol-level ("your code calls this") result, otherwise it is module-level.
osvs, called, present = {}, set(), set()
decoder = json.JSONDecoder()
raw = open(stream_path).read()
idx = 0
while idx < len(raw):
    while idx < len(raw) and raw[idx].isspace():
        idx += 1
    if idx >= len(raw):
        break
    obj, idx = decoder.raw_decode(raw, idx)
    if "osv" in obj:
        osvs[obj["osv"]["id"]] = obj["osv"]
    elif "finding" in obj:
        f = obj["finding"]
        osv_id = f.get("osv")
        if not osv_id:
            continue
        present.add(osv_id)
        frames = f.get("trace") or []
        if frames and frames[0].get("function"):
            called.add(osv_id)

try:
    allow_raw = json.load(open(allow_path))
    allow = {e["id"]: e for e in allow_raw.get("allow", [])}
except FileNotFoundError:
    allow = {}
except (KeyError, ValueError) as exc:
    print(f"vuln-gate: {allow_path} is malformed: {exc}")
    sys.exit(2)

today = datetime.date.today().isoformat()
expired = [e for e in allow.values() if not e.get("reviewBy") or e["reviewBy"] < today]
expired_ids = {e["id"] for e in expired}

def summary(osv_id):
    osv = osvs.get(osv_id, {})
    mods = sorted({a["package"]["ecosystem"] and a["package"]["name"]
                   for a in osv.get("affected", []) if a.get("package")})
    return f"{osv_id} ({', '.join(mods) or 'unknown module'}) — {osv.get('summary', '').strip()}"

violations = sorted(i for i in called if i not in allow or i in expired_ids)
accepted = sorted(i for i in called if i in allow and i not in expired_ids)
informational = sorted(present - called)

for i in accepted:
    print(f"vuln-gate ACCEPTED {summary(i)}")
    print(f"                   reason: {allow[i]['reason']} (review by {allow[i]['reviewBy']})")
for i in informational:
    print(f"vuln-gate INFO     {summary(i)} — in the tree, not called")
for e in expired:
    print(f"vuln-gate EXPIRED  {e['id']} (reviewBy {e.get('reviewBy', 'unset')}) — re-review required")
for i in violations:
    print(f"vuln-gate FAIL     {summary(i)}")

if violations or expired:
    print(f"vuln-gate: {len(violations)} unaccepted vulnerability(ies), {len(expired)} expired exception(s)")
    print("Fix the dependency, or add an entry to .vuln-allowlist.json with a reason and a reviewBy.")
    sys.exit(1)
print(f"vuln-gate: clean ({len(accepted)} accepted, {len(informational)} present but not called)")
PY
