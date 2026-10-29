#!/usr/bin/env bash
# Builds the release files for one version into agent/dist/<version>:
# hedera_<os>_<arch> for macOS and Linux, and the manifest.json agents read
# (see agent/internal/update). Run by .github/workflows/release.yml, and by
# `scripts/release.sh <version> --dry-run`.
#
#   scripts/build-release.sh v0.1.0
set -euo pipefail

die() { echo "build-release: $*" >&2; exit 1; }

version=${1:-}
[[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "usage: scripts/build-release.sh vX.Y.Z"

cd "$(git rev-parse --show-toplevel)"
dist=agent/dist/$version
rm -rf "$dist"
mkdir -p "$dist"
entries=()
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
	os=${target%/*} arch=${target#*/}
	name=hedera_${os}_${arch}
	echo "building $name"
	# Without cgo the Go linker ad-hoc signs the macOS builds itself, so they
	# run on Apple silicon even when cross-compiled on Linux.
	(cd agent && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "dist/$version/$name" ./cmd/hedera)
	sum=$(shasum -a 256 "$dist/$name" | cut -d' ' -f1)
	entries+=("    \"$name\": \"$sum\"")
done
{
	echo '{'
	echo "  \"version\": \"$version\","
	echo '  "files": {'
	(IFS=$'\n'; echo "${entries[*]}" | sed '$!s/$/,/')
	echo '  }'
	echo '}'
} >"$dist/manifest.json"

native=$dist/hedera_$(go env GOOS)_$(go env GOARCH)
[[ ! -x $native ]] || "$native" version | grep -q " $version " || die "$native does not report $version"
echo "built $dist"
