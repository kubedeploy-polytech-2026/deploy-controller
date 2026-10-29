#!/usr/bin/env bash
set -euo pipefail
version=0.75.0
case "$(uname -m)" in
  x86_64) architecture=64bit ;;
  aarch64|arm64) architecture=ARM64 ;;
  *) echo 'Unsupported Trivy architecture' >&2; exit 1 ;;
esac
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
archive="trivy_${version}_Linux-${architecture}.tar.gz"
# Public release assets do not need a repository-scoped GitHub token.
release_url="https://github.com/aquasecurity/trivy/releases/download/v$version"
for file in "$archive" "trivy_${version}_checksums.txt"; do
  curl --fail --silent --show-error --location --retry 3 \
    --connect-timeout 20 --max-time 180 \
    --output "$work_dir/$file" "$release_url/$file"
done
(cd "$work_dir" && sha256sum --check --ignore-missing "trivy_${version}_checksums.txt")
mkdir -p .tools/bin
tar -xzf "$work_dir/$archive" -C .tools/bin trivy
