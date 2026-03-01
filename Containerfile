# syntax=docker/dockerfile:1.7

# -------- Build Stage --------
FROM golang:1.25-alpine AS builder

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

# Copy go mod files first (better layer caching)
COPY go.mod go.sum ./

# 👇 Use SSH mount for private repo access
RUN --mount=type=ssh \
    go mod download

# Copy source
COPY cmd/ cmd/
COPY internal/ internal/

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -a -o manager ./cmd/main.go


# -------- Runtime Stage --------
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/manager /manager

USER nonroot:nonroot

ENTRYPOINT ["/manager"]