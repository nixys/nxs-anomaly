#!/usr/bin/env sh
# Stamps a new release version into every file that carries it, from the single
# source of truth (./VERSION). The inverse of scripts/check-version.sh — see that
# file for which string goes where and why the image tag keeps its `v`.
#
#   scripts/set-version.sh 0.1.29
#   git commit -am 'chore(release): 0.1.29' && git tag v0.1.29
#
# The pre-push hook refuses to push a tag that disagrees with VERSION, so the
# bump commit must land first.
set -eu

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

version="${1:-}"
version="${version#v}"
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "usage: set-version.sh <X.Y.Z>" >&2
	exit 2
fi

previous="$(tr -d '[:space:]' <VERSION 2>/dev/null || true)"

printf '%s\n' "$version" >VERSION

chart=deploy/helm/nxs-anomaly/Chart.yaml
sed -i \
	-e "s/^version:.*/version: $version/" \
	-e "s/^appVersion:.*/appVersion: \"v$version\"/" \
	"$chart"

# Only info.version, which lives in the first lines of the document; the string
# "version" also occurs inside schema descriptions further down.
sed -i "1,20s/\(\"version\"[[:space:]]*:[[:space:]]*\)\"[0-9][^\"]*\"/\1\"$version\"/" docs/openapi.json

# The chart README carries the version in one shell assignment; the examples read
# it from there. Rewrite that line, and any literal a hand edit left behind.
readme=deploy/helm/nxs-anomaly/README.md
sed -i -E "s/[0-9]+\.[0-9]+\.[0-9]+/$version/g" "$readme"

if [ -n "$previous" ] && [ "$previous" != "$version" ]; then
	echo "set-version: $previous → $version"
else
	echo "set-version: $version"
fi
exec "$root/scripts/check-version.sh"
