# Builds the image from source, for development and trying changes on a
# cluster. Releases use Dockerfile.release, which copies goreleaser's binary.
ARG BASE_IMAGE=golang:1.27@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc
FROM ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags '-s -w' -o orca-tasks ./cmd/orca-tasks

FROM gcr.io/distroless/static:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=builder /workspace/orca-tasks /orca-tasks
# The attachment directory, owned by the nonroot user so a Docker volume mounted
# there starts out writable.
COPY --chown=65532:65532 .config/docker/data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/orca-tasks"]
CMD ["serve"]
