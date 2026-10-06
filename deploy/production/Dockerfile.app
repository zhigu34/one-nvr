# Use the bundled BuildKit frontend; avoid an extra Docker Hub frontend pull.
ARG GO_IMAGE=golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195
ARG RUNTIME_IMAGE=debian:bookworm-20260918-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251
FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=one-nvr-go-mod-1.27-amd64,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
COPY deploy/production/templates ./deploy/production/templates
RUN --mount=type=cache,id=one-nvr-go-mod-1.27-amd64,target=/go/pkg/mod \
    --mount=type=cache,id=one-nvr-go-build-1.27-amd64,target=/root/.cache/go-build \
    mkdir /out && go build -trimpath -buildvcs=false -o /out/api ./cmd/api && \
    go build -trimpath -buildvcs=false -o /out/worker ./cmd/worker && \
    go build -trimpath -buildvcs=false -o /out/admin ./cmd/admin && \
    go build -trimpath -buildvcs=false -o /out/media-launcher ./cmd/media-launcher
FROM build AS media-test-build
COPY tests/media ./tests/media
RUN --mount=type=cache,id=one-nvr-go-mod-1.27-amd64,target=/go/pkg/mod \
    --mount=type=cache,id=one-nvr-go-build-1.27-amd64,target=/root/.cache/go-build \
    go build -trimpath -buildvcs=false -o /out/media-test ./tests/media
FROM ${RUNTIME_IMAGE} AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ARG ONE_NVR_DEBIAN_SNAPSHOT_URL=https://snapshot.debian.org/archive/debian/20260919T000000Z/
COPY deploy/production/media-packages.lock deploy/production/install-media.sh /tmp/
RUN sh /tmp/install-media.sh && rm /tmp/install-media.sh /tmp/media-packages.lock
COPY --from=build /out/ /usr/local/bin/
USER 10001:10001
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/api"]
FROM runtime AS media-test
COPY --from=media-test-build /out/media-test /usr/local/bin/media-test
ENTRYPOINT ["/usr/local/bin/media-test"]
FROM runtime AS app
