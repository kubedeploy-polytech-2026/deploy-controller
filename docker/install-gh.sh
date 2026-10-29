#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=docker/tools.lock
source /build/tools.lock
case "${1:?Set target architecture}" in
  amd64) checksum="$GH_AMD64_SHA256" ;;
  arm64) checksum="$GH_ARM64_SHA256" ;;
  *) echo 'Unsupported architecture' >&2; exit 1 ;;
esac
mkdir -p /usr/local/bin
archive="gh_${GH_VERSION}_linux_$1"
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
curl --fail --silent --show-error --location --retry 3 --connect-timeout 20 --max-time 180 \
  "https://github.com/cli/cli/releases/download/v$GH_VERSION/$archive.tar.gz" -o "$work_dir/gh.tar.gz"
printf '%s  %s\n' "$checksum" "$work_dir/gh.tar.gz" | sha256sum -c
tar -xzf "$work_dir/gh.tar.gz" -C /usr/local/bin --strip-components=2 "$archive/bin/gh"
license="$(tar -tzf "$work_dir/gh.tar.gz" | grep -E '(^|/)LICENSE$')"
[[ -n "$license" && "$license" != *$'\n'* ]]
tar -xzf "$work_dir/gh.tar.gz" -C "$work_dir" "$license"
mkdir -p /usr/share/licenses/controller-tools
cp "$work_dir/$license" /usr/share/licenses/controller-tools/gh
