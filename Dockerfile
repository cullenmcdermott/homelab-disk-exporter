# Build stage
FROM golang:1.26-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o disk-exporter .

# Runtime stage — needs smartmontools for SATA SMART data via smartctl
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    smartmontools \
    ca-certificates \
    wget \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/disk-exporter /disk-exporter

EXPOSE 9999

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:9999/healthz || exit 1

ENTRYPOINT ["/disk-exporter"]
