# Build stage — compiles a static manager binary.
# TARGETOS / TARGETARCH are injected automatically by BuildKit (and by
# `docker buildx --platform`), so the same Dockerfile produces amd64 and
# arm64 images without per-arch branches.
FROM golang:1.26-alpine AS builder
WORKDIR /workspace

# Cache module downloads separately from source changes.
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://goproxy.cn,direct && go mod download


COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
# VERSION is passed in from the Makefile (git describe). Injected into the
# binary via -ldflags so `kubectl logs` shows what's running.
ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -trimpath \
  -ldflags "-s -w -X github.com/satoukick/clustersecret-go/cmd.version=${VERSION}" \
  -o /manager ./cmd

# Runtime stage — distroless static, nonroot. No shell, no package manager,
# runs as UID 65532 by default, satisfying Pod Security "restricted".
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /manager /manager
# Numeric UID/GID, not the "nonroot" name. kubelet can only verify
# runAsNonRoot when the image USER is numeric — a name like "nonroot"
# makes it refuse to start the container with:
#   "image has non-numeric user (nonroot), cannot verify user is non-root"
USER 65532:65532
# metrics (:8080) + health/ready probe (:8081)
EXPOSE 8080 8081
ENTRYPOINT ["/manager"]
