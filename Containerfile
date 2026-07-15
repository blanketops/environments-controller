# syntax=docker/dockerfile:1.7

# -------- Build Stage --------
# Use the host platform for the builder so Go cross-compiles natively
# instead of running the compiler under QEMU emulation.
# GO_VERSION is injected from go.mod via the image.yml build-arg.
ARG GO_VERSION=1.26.3
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install git for private modules
RUN apk add --no-cache git

# Go private module configuration
ENV GOPRIVATE=github.com/blanketops/*
ENV GONOSUMDB=github.com/blanketops/*
ENV GOPROXY=direct

# Copy go mod files first (better layer caching)
COPY go.mod go.sum ./

# PAT-authenticated HTTPS download. git config + go mod download run in a
# single layer so the token in ~/.gitconfig never persists in a later layer.
RUN --mount=type=secret,id=gh_pat \
    git config --global url."https://$(cat /run/secrets/gh_pat)@github.com/".insteadOf "https://github.com/" && \
    go mod download && \
    rm -f /root/.gitconfig

# Copy source
COPY cmd/ cmd/
COPY internal/ internal/

# Cross-compile for the target platform natively on the build host
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -a -o manager ./cmd

# -------- Runtime Stage --------
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/manager /manager

USER nonroot:nonroot

ENTRYPOINT ["/manager"]
