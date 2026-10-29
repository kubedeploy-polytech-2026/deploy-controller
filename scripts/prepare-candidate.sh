#!/usr/bin/env bash
set -euo pipefail
: "${RELEASE_TAG:?Set RELEASE_TAG}"
: "${ARCH:?Set ARCH}"
: "${GITHUB_SHA:?Set GITHUB_SHA}"
: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY}"
: "${IMAGE:=kubedeploy-candidate:build}"
.tools/bin/trivy image --quiet --skip-db-update --format cyclonedx --output sbom.cdx.json "$IMAGE"
docker image inspect "$IMAGE" > image-inspect.json
docker save "$IMAGE" | gzip -1n > image.tar.gz
python3 scripts/release_candidate.py create . --repository "$GITHUB_REPOSITORY" \
  --revision "$GITHUB_SHA" --tag "$RELEASE_TAG" --arch "$ARCH" --inspect image-inspect.json
