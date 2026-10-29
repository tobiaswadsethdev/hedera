#!/usr/bin/env bash
# Tags a release. Pushing the tag starts .github/workflows/release.yml, which
# builds hedera for macOS and Linux and publishes a GitHub release that
# running agents pick up by themselves (see agent/internal/update).
#
#   scripts/release.sh v0.1.0             tag origin/main's HEAD and push the tag
#   scripts/release.sh v0.1.0 --dry-run   build into agent/dist/v0.1.0 only
#
# Versions with a suffix (v0.2.0-rc.1) are published as pre-releases, which
# agents ignore.
set -euo pipefail

die() { echo "release: $*" >&2; exit 1; }

version=${1:-}
dry_run=${2:-}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "usage: scripts/release.sh vX.Y.Z [--dry-run]"
[[ -z $dry_run || $dry_run == --dry-run ]] || die "unknown option $dry_run"

cd "$(git rev-parse --show-toplevel)"
if [[ -n $dry_run ]]; then
	exec scripts/build-release.sh "$version"
fi

git fetch -q origin main --tags
[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || die "HEAD is not origin/main; release from an up-to-date main"
[[ -z $(git status --porcelain) ]] || die "the working tree has uncommitted changes"
! git rev-parse -q --verify "refs/tags/$version" >/dev/null || die "tag $version already exists"

git tag -a "$version" -m "hedera $version"
git push origin "$version"
echo "pushed $version; follow the build with: gh run watch \$(gh run list --workflow release.yml --limit 1 --json databaseId -q '.[0].databaseId')"
