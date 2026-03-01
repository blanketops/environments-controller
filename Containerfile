# -------- Build Stage --------
FROM golang:1.25-alpine AS builder

WORKDIR /workspace

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager ./cmd/main.go


# -------- Runtime Stage --------
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/manager /manager

USER nonroot:nonroot

ENTRYPOINT ["/manager"]