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

# One heading per version in the CHANGELOG. set-version.sh renames the top
# "[Unreleased]" heading; an unanchored substitution once renamed an older
# "[Unreleased]" section too, labelling years-old entries as this release (1.3.0
# and again 1.5.0). That older section is now "[Unreleased — history before
# 1.2.0]", so a top "[Unreleased]" between releases no longer trips this check.
changelog=CHANGELOG.md
dupe_marker="$(mktemp)"
read_file "$changelog" | grep -oE '^## \[[^]]+\]' | sort | uniq -d | while IFS= read -r d; do
	echo "check-version: $changelog has more than one '$d' heading — the release rename hit the historical section too" >&2
	echo dup >>"$dupe_marker"
done
[ -s "$dupe_marker" ] && rc=1
rm -f "$dupe_marker"

# The Go toolchain: go.mod's `go` directive is the single source of truth. Every
# CI/image pin must match it — a drift built binaries with an older stdlib than
# go.mod promised, and broke golangci-lint on export data (1.9.24).
gomod_go="$(read_file go.mod | sed -n 's/^go[[:space:]]\{1,\}\([0-9.]*\)$/\1/p' | head -1)"
check_go_pin() { # file, found value
	[ "$2" = "$gomod_go" ] ||
		fail "$1 pins Go '$2', expected '$gomod_go' (go.mod). Bump them together."
}
# GO_VERSION: in the GitLab pipeline and the GitHub workflows. The workflows sit
# under packaging/community/ here and under .github/ in the community tree, which
# has no .gitlab-ci.yml — only files that exist are checked.
for f in .gitlab-ci.yml packaging/community/.github/workflows/*.yml .github/workflows/*.yml; do
	[ -f "$f" ] || continue
	v="$(read_file "$f" | sed -n "s/^[[:space:]]*GO_VERSION:[[:space:]]*['\"]\{0,1\}\([0-9.]*\).*/\1/p" | head -1)"
	if [ -n "$v" ]; then check_go_pin "$f" "$v"; fi
done
if [ -f Dockerfile ]; then
	v="$(read_file Dockerfile | sed -n 's/^FROM golang:\([0-9.]*\)-.*/\1/p' | head -1)"
	if [ -n "$v" ]; then check_go_pin Dockerfile "$v"; fi
fi

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
