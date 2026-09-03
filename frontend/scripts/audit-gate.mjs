#!/usr/bin/env node
// audit-gate.mjs — supply-chain gate over `npm audit --json`.
//
// Why not raw `npm audit --audit-level=high`? Some advisories have no fixed
// version yet but are unreachable in how we actually use the dependency (e.g.
// React Router SSR/RSC advisories in a pure client-side SPA). A raw gate would
// block CI forever. This gate fails on any advisory at/above the threshold
// EXCEPT ones explicitly listed in .audit-allowlist.json with a reason and a
// reviewBy date — and it fails if any allowlist entry is past its reviewBy, so
// exceptions cannot rot silently.
//
// Usage: node scripts/audit-gate.mjs --level=high [--omit-dev]
//   --level      minimum severity that fails the gate (default: high)
//   --omit-dev   audit production dependencies only (adds --omit=dev)

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const ORDER = ['info', 'low', 'moderate', 'high', 'critical'];
const args = process.argv.slice(2);
const level = (args.find((a) => a.startsWith('--level=')) || '--level=high').split('=')[1];
const omitDev = args.includes('--omit-dev');
const threshold = ORDER.indexOf(level);
if (threshold < 0) {
  console.error(`unknown --level=${level}; expected one of ${ORDER.join(', ')}`);
  process.exit(2);
}

const here = dirname(fileURLToPath(import.meta.url));
const scope = omitDev ? 'production' : 'all';

// Load the allowlist (optional).
let allow = [];
try {
  const raw = JSON.parse(readFileSync(join(here, '..', '.audit-allowlist.json'), 'utf8'));
  allow = Array.isArray(raw.allow) ? raw.allow : [];
} catch {
  /* no allowlist file — every advisory is a violation */
}

// Fail closed on expired exceptions before we even look at advisories.
const today = new Date().toISOString().slice(0, 10);
const expired = allow.filter((e) => !e.reviewBy || e.reviewBy < today);
const allowById = new Map(allow.filter((e) => !expired.includes(e)).map((e) => [e.ghsa, e]));

// Run npm audit. It exits non-zero when vulnerabilities exist; we only care
// about the JSON body, so capture stdout regardless of exit code.
const auditArgs = ['audit', '--json'];
if (omitDev) auditArgs.push('--omit=dev');
let out;
try {
  out = execFileSync('npm', auditArgs, { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 });
} catch (e) {
  out = e.stdout || '';
}
let report;
try {
  report = JSON.parse(out);
} catch {
  console.error('audit-gate: could not parse `npm audit --json` output');
  process.exit(2);
}

// Collect distinct advisories at/above the threshold.
const seen = new Map(); // ghsa -> {severity, pkg, url}
for (const [pkg, v] of Object.entries(report.vulnerabilities || {})) {
  for (const via of v.via || []) {
    if (typeof via !== 'object' || !via.url) continue;
    if (ORDER.indexOf(via.severity) < threshold) continue;
    const ghsa = via.url.split('/').pop();
    if (!seen.has(ghsa)) seen.set(ghsa, { severity: via.severity, pkg, url: via.url, title: via.title });
  }
}

const violations = [];
const accepted = [];
for (const [ghsa, a] of seen) {
  (allowById.has(ghsa) ? accepted : violations).push({ ghsa, ...a });
}

const banner = `audit-gate [${scope}, level>=${level}]`;
for (const a of accepted) {
  console.log(`${banner} ACCEPTED ${a.severity} ${a.ghsa} (${a.pkg}) — ${allowById.get(a.ghsa).reason}`);
}
for (const e of expired) {
  console.error(`${banner} EXPIRED exception ${e.ghsa} (reviewBy ${e.reviewBy || 'unset'}) — re-review required`);
}
for (const a of violations) {
  console.error(`${banner} FAIL ${a.severity} ${a.ghsa} (${a.pkg}) ${a.url}`);
}

if (violations.length || expired.length) {
  console.error(`${banner}: ${violations.length} unaccepted advisory(ies), ${expired.length} expired exception(s)`);
  process.exit(1);
}
console.log(`${banner}: clean (${accepted.length} accepted exception(s))`);
