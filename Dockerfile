FROM golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd AS builder
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/deploy-controller ./cmd/controller

FROM builder AS tool-builder
RUN apk add --no-cache bash ca-certificates curl
COPY docker/tools.lock docker/build-score.sh docker/build-kubectl.sh /build/
COPY docker/kubectl/go.mod /build/kubectl/go.mod
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build bash /build/build-score.sh
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build bash /build/build-kubectl.sh

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
ARG TARGETARCH
RUN apk upgrade --no-cache && apk add --no-cache bash ca-certificates curl git make python3
COPY docker/tools.lock docker/install-gh.sh /build/
RUN bash /build/install-gh.sh "$TARGETARCH" && rm -rf /build
COPY --from=tool-builder /out/ /usr/local/bin/
RUN mkdir -p /state /usr/share/licenses/controller-tools \
    && mv /usr/local/bin/licenses/* /usr/share/licenses/controller-tools/ \
    && rmdir /usr/local/bin/licenses \
    && chown 65532:65532 /state \
    && chmod 700 /state
COPY --chmod=755 docker/entrypoint.sh /usr/local/bin/controller-entrypoint
ENV HOME=/tmp/home XDG_CACHE_HOME=/tmp/cache GH_CONFIG_DIR=/tmp/gh DOCKER_CONFIG=/tmp/docker
USER 65532:65532
WORKDIR /state
ENTRYPOINT ["/usr/local/bin/controller-entrypoint"]
CMD ["--execute", "--state-dir", "/state"]
