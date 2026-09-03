#!/usr/bin/env sh
# BETA-001 release gate: prove the frontend production build is deterministic and
# self-consistent in a clean environment. Runs two clean `npm ci && npm run build`
# passes from the committed lockfile and asserts the emitted dist is byte-identical,
# then smoke-checks that index.html references the hashed assets that were emitted.
#
# Run from the frontend/ directory (CI does `cd frontend` first):
#   sh scripts/verify-build.sh
set -eu

here="$(pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Copy only the build inputs — never node_modules or a stale dist — so each pass is
# a true clean build from the lockfile.
for p in package.json package-lock.json tsconfig.json vite.config.ts index.html src; do
  cp -R "$here/$p" "$work/"
done
cd "$work"

build_pass() {
  rm -rf node_modules dist
  npm ci --no-audit --no-fund >/dev/null
  # tsc -b --noEmit then vite build. Keep the output instead of discarding it: a
  # type error printed to stdout used to vanish into /dev/null, leaving the job
  # with "clean build 1/2" and exit 1 as its entire explanation.
  npm run build >build.log 2>&1 || { cat build.log >&2; return 1; }
  find dist -type f -exec sha256sum {} \; | sed 's#dist/##' | sort
}

echo "verify-build: clean build 1/2"
build_pass >manifest1.txt
echo "verify-build: clean build 2/2"
build_pass >manifest2.txt

if ! diff -u manifest1.txt manifest2.txt; then
  echo "verify-build: FAIL — two clean builds are not byte-identical (non-deterministic)" >&2
  exit 1
fi

# Asset smoke: index.html must reference at least one hashed JS bundle that exists.
asset="$(grep -oE 'assets/[^"]+\.js' dist/index.html | head -n1 || true)"
if [ -z "$asset" ] || [ ! -f "dist/$asset" ]; then
  echo "verify-build: FAIL — index.html does not reference an emitted JS asset" >&2
  exit 1
fi

echo "verify-build: OK — deterministic build ($(wc -l <manifest1.txt) files), index.html -> $asset"
