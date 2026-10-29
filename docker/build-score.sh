#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=docker/tools.lock
source /build/tools.lock
mkdir -p /build/score /out/licenses
curl --fail --silent --show-error --location --retry 3 --connect-timeout 20 --max-time 180 \
  "https://codeload.github.com/score-spec/score-k8s/tar.gz/$SCORE_REVISION" -o /build/score.tar.gz
printf '%s  %s\n' "$SCORE_SOURCE_SHA256" /build/score.tar.gz | sha256sum -c
tar -xzf /build/score.tar.gz --strip-components=1 -C /build/score
cd /build/score
go mod edit -go=1.27.3 -toolchain=none -require=golang.org/x/net@v0.60.0
go mod tidy
CGO_ENABLED=0 go build -p 2 -trimpath -buildvcs=false \
  -ldflags="-s -w -X github.com/score-spec/score-k8s/internal/version.Version=$SCORE_VERSION -X github.com/score-spec/score-k8s/internal/version.GitCommit=$SCORE_REVISION" \
  -o /out/score-k8s ./cmd/score-k8s
cp LICENSE /out/licenses/score-k8s
