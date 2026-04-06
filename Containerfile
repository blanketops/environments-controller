# syntax=docker/dockerfile:1.7

# -------- Build Stage --------
# Use the host platform for the builder so Go cross-compiles natively
# instead of running the compiler under QEMU emulation
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install git + ssh for private modules
RUN apk add --no-cache git openssh

# Configure SSH directory
RUN mkdir -p /root/.ssh && chmod 700 /root/.ssh

# Pre-populate known_hosts to avoid host verification issues
RUN ssh-keyscan github.com >> /root/.ssh/known_hosts

# Go private module configuration
ENV GOPRIVATE=github.com/ntlaletsi70/*
ENV GONOSUMDB=github.com/ntlaletsi70/*
ENV GOPROXY=direct

# Rewrite HTTPS to SSH for private org
RUN git config --global url."git@github.com:ntlaletsi70/".insteadOf "https://github.com/ntlaletsi70/"

# Copy go mod files first (better layer caching)
COPY go.mod go.sum ./

# Use SSH mount for private repo access
RUN --mount=type=ssh \
    go mod download

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
