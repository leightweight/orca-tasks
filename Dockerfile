# Builds the image from source, for development and trying changes on a
# cluster. Releases use Dockerfile.release, which copies goreleaser's binary.
ARG BASE_IMAGE=golang:1.27@sha256:0c050feb47802ae436b9030833694a0b80160d08e3b3049ad30bf7c9e724e09d
FROM ${BASE_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags '-s -w' -o orca-tasks ./cmd/orca-tasks

FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=builder /workspace/orca-tasks /orca-tasks
# The attachment directory, owned by the nonroot user so a Docker volume mounted
# there starts out writable.
COPY --chown=65532:65532 .config/docker/data /data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/orca-tasks"]
CMD ["serve"]
