#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=docker/tools.lock
source /build/tools.lock
cd /build/kubectl
mkdir -p /out/licenses
curl --fail --silent --show-error --location --retry 3 --connect-timeout 20 --max-time 180 \
  "https://raw.githubusercontent.com/kubernetes/kubernetes/v$KUBECTL_VERSION/cmd/kubectl/kubectl.go" -o main.go
printf '%s  %s\n' "$KUBECTL_MAIN_SHA256" main.go | sha256sum -c
go mod tidy
CGO_ENABLED=0 go build -p 2 -trimpath -buildvcs=false \
  -ldflags="-s -w -X k8s.io/component-base/version.gitVersion=v$KUBECTL_VERSION -X k8s.io/component-base/version.gitMajor=1 -X k8s.io/component-base/version.gitMinor=36 -X k8s.io/component-base/version.gitTreeState=dirty" \
  -o /out/kubectl .
cp /go/pkg/mod/k8s.io/kubectl@v0.36.5/LICENSE /out/licenses/kubectl
