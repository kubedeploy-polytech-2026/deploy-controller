#!/usr/bin/env bash
set -euo pipefail
: "${RELEASE_TAG:?Set RELEASE_TAG}"
: "${REVISION:?Set REVISION}"
: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY}"
: "${GITHUB_OUTPUT:?Set GITHUB_OUTPUT}"
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"
arches=(amd64 arm64)
ids=()
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT
for arch in "${arches[@]}"; do
  id="$(python3 "$project_root/scripts/release_candidate.py" check "candidates/$arch" \
    --repository "$GITHUB_REPOSITORY" --revision "$REVISION" --tag "$RELEASE_TAG" --arch "$arch")"
  ids+=("$id")
done
for index in 0 1; do
  arch="${arches[$index]}"
  docker load --input "candidates/$arch/image.tar.gz"
  docker image inspect kubedeploy-candidate:build > "$work_dir/inspect.json"
  python3 "$project_root/scripts/release_candidate.py" check "candidates/$arch" \
    --repository "$GITHUB_REPOSITORY" --revision "$REVISION" --tag "$RELEASE_TAG" \
    --arch "$arch" --inspect "$work_dir/inspect.json" >/dev/null
done
mkdir -p dist
for index in 0 1; do
  arch="${arches[$index]}"
  docker tag "${ids[$index]}" "$image:$RELEASE_TAG-$arch"
  docker push --platform "linux/$arch" "$image:$RELEASE_TAG-$arch"
  digest="$(docker buildx imagetools inspect "$image:$RELEASE_TAG-$arch" --format '{{json .}}' | jq -er '.manifest.digest')"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]]
  echo "$arch=$digest" >> "$GITHUB_OUTPUT"
  cp "candidates/$arch/sbom.cdx.json" "dist/sbom-$arch.cdx.json"
  cp "candidates/$arch/trivy.json" "dist/trivy-$arch.json"
done
docker buildx imagetools create --tag "$image:$RELEASE_TAG" --tag "$image:sha-$REVISION" \
  "$image:$RELEASE_TAG-amd64" "$image:$RELEASE_TAG-arm64"
digest="$(docker buildx imagetools inspect "$image:$RELEASE_TAG" --format '{{json .}}' | jq -er '.manifest.digest')"
sha_digest="$(docker buildx imagetools inspect "$image:sha-$REVISION" --format '{{json .}}' | jq -er '.manifest.digest')"
[[ "$digest" =~ ^sha256:[a-f0-9]{64}$ && "$digest" == "$sha_digest" ]]
echo "image=$image" >> "$GITHUB_OUTPUT"
echo "digest=$digest" >> "$GITHUB_OUTPUT"
jq -n --arg repository "$GITHUB_REPOSITORY" --arg tag "$RELEASE_TAG" \
  --arg revision "$REVISION" --arg image "$image" --arg digest "$digest" \
  '{schema_version: "1.0.0", repository: $repository, tag: $tag, revision: $revision,
    image: $image, digest: $digest, platforms: ["linux/amd64", "linux/arm64"]}' > dist/release.json
