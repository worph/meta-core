# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o meta-core ./cmd/meta-core

# Runtime stage
FROM alpine:3.19

# Container registry metadata
LABEL org.opencontainers.image.source=https://github.com/worph/meta-core
LABEL org.opencontainers.image.description="MetaMesh meta-core sidecar - leader election, Redis management, HTTP API"
LABEL org.opencontainers.image.licenses=MIT

# Install runtime dependencies
RUN apk add --no-cache \
    redis \
    bash \
    ca-certificates

# Create non-root user
RUN addgroup -S metacore && adduser -S metacore -G metacore

# Copy binary from builder
COPY --from=builder /build/meta-core /usr/local/bin/meta-core

# Set permissions
RUN chmod +x /usr/local/bin/meta-core

# Create directories
RUN mkdir -p /meta-core/locks /meta-core/db/redis /meta-core/services && \
    chown -R metacore:metacore /meta-core

# Switch to non-root user
USER metacore

# Environment defaults
ENV META_CORE_PATH=/meta-core \
    FILES_PATH=/files \
    SERVICE_NAME=meta-core \
    META_CORE_HTTP_PORT=9000 \
    REDIS_PORT=6379

# Expose HTTP API port
EXPOSE 9000

# Health check
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:9000/health || exit 1

# Run the binary
ENTRYPOINT ["/usr/local/bin/meta-core"]
