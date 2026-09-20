#!/usr/bin/env sh
# Validates that every place carrying the release version agrees with ./VERSION,
# which is the single source of truth. Shared by the local hooks
# (.githooks/pre-commit, .githooks/pre-push) and the CI job `test:version`.
#
# Usage:
#   check-version.sh                 # check the working tree
#   check-version.sh --staged        # check what a commit would contain (index)
#   check-version.sh --tag v1.2.3    # additionally require the tag to match VERSION
#
# The canonical strings, all derived from VERSION=X.Y.Z:
#
#   git tag              vX.Y.Z      what CI reacts to ($CI_COMMIT_TAG)
#   image tags           vX.Y.Z      the edition's images (see the naming policy in
#                                    .gitlab-ci.yml: the edition is the last name segment)
#   chart appVersion     vX.Y.Z      values.image.tag defaults to .Chart.AppVersion,
#                                    so this IS the image tag a render resolves
#   chart version        X.Y.Z       chart versions must be SemVer, hence no `v`
#   openapi info.version X.Y.Z
#
# The `v` on the image tag is not cosmetic: `release:helm-chart` packages with
# `--app-version "$CI_COMMIT_TAG"` and `.buildkit` pushes `${IMAGE_NAME}:${CI_COMMIT_TAG}`,
# both with the prefix. A committed appVersion without it makes `helm install` from
# a git checkout resolve an image tag that was never published.
#
# frontend/package.json is deliberately NOT part of this contract: the package is
# private, is never published, and its version is duplicated in package-lock.json,
# where a mismatch breaks `npm ci`.
set -eu

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

staged=0
tag=''
while [ $# -gt 0 ]; do
	case "$1" in
	--staged) staged=1 ;;
	--tag)
		shift
		tag="${1:-}"
		;;
	*)
		echo "check-version: unknown argument: $1" >&2
		exit 2
		;;
	esac
	shift
done

# Read a tracked file either from the index (what the commit will contain) or from
# the working tree. `git show :path` always resolves for a tracked file — for an
# unmodified one the index entry still holds the HEAD content.
# A file that is not in the index yet (a new one, staged or not) falls back to the
# working tree — otherwise the very commit that introduces it cannot pass.
read_file() {
	if [ "$staged" -eq 1 ] && git show ":$1" 2>/dev/null; then
		return 0
	fi
	cat "$1"
}

rc=0
fail() {
	echo "check-version: $1" >&2
	rc=1
}

version="$(read_file VERSION | tr -d '[:space:]')"
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "check-version: VERSION must be a bare X.Y.Z (got '$version')" >&2
	exit 1
fi

chart=deploy/helm/nxs-anomaly/Chart.yaml
chart_version="$(read_file "$chart" | sed -n 's/^version:[[:space:]]*\(.*\)$/\1/p' | tr -d '"'"'"' ')"
chart_app="$(read_file "$chart" | sed -n 's/^appVersion:[[:space:]]*\(.*\)$/\1/p' | tr -d '"'"'"' ')"
[ "$chart_version" = "$version" ] ||
	fail "$chart version is '$chart_version', expected '$version'"
[ "$chart_app" = "v$version" ] ||
	fail "$chart appVersion is '$chart_app', expected 'v$version' (it is the published image tag)"

spec=docs/openapi.json
spec_version="$(read_file "$spec" | sed -n '1,20p' | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
[ "$spec_version" = "$version" ] ||
	fail "$spec info.version is '$spec_version', expected '$version'"

# The chart README documents install/verify commands. Every version literal in it
# must be the current one — a stale one hands operators a pull that 404s.
readme=deploy/helm/nxs-anomaly/README.md
for lit in $(read_file "$readme" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | sort -u); do
	[ "$lit" = "$version" ] ||
		fail "$readme mentions version '$lit', expected only '$version' (examples take it from the VERSION= line)"
done

# One heading per version in the CHANGELOG. The release step renames the
# "[Unreleased]" heading with sed, and the file carries a second, historical
# "[Unreleased]" section further down: an unanchored substitution renamed both,
# labelling years-old entries as this release. It happened for 1.3.0 and again
# for 1.5.0.
changelog=CHANGELOG.md
dupe_marker="$(mktemp)"
read_file "$changelog" | grep -oE '^## \[[^]]+\]' | sort | uniq -d | while IFS= read -r d; do
	echo "check-version: $changelog has more than one '$d' heading — the release rename hit the historical section too" >&2
	echo dup >>"$dupe_marker"
done
[ -s "$dupe_marker" ] && rc=1
rm -f "$dupe_marker"

if [ -n "$tag" ]; then
	[ "$tag" = "v$version" ] ||
		fail "tag '$tag' does not match VERSION ('$version' → expected tag 'v$version'). Run scripts/set-version.sh ${tag#v} and commit before tagging."
fi

if [ "$rc" -ne 0 ]; then
	echo "check-version: fix with  scripts/set-version.sh <X.Y.Z>" >&2
	exit 1
fi

if [ -n "$tag" ]; then
	echo "check-version: ok — $version (tag $tag, images :v$version)"
else
	echo "check-version: ok — $version"
fi
