# =============================================================================
# meta-core Standalone Container
# Multi-stage build for meta-core as a standalone service
#
# Includes:
# - Bash-based leader election script (flock on shared volume)
# - meta-core Go binary (HTTP API, Mount API, Watcher)
# - nginx (dashboard, WebDAV, API proxy)
# - Redis (managed by supervisord on leader)
# - rclone (single mount executor — SMB goes through rclone's :smb: backend)
# - supervisord (process management - runs on leader only)
#
# Architecture:
# - leader-election.sh handles flock, starts supervisord on leader
# - Followers remain dormant, retry flock every 5s
# - Only leader runs Redis, meta-core, nginx, rclone, mount-watcher
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build Go binary
# -----------------------------------------------------------------------------
FROM golang:1.21-alpine AS go-builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go mod files first for caching
COPY go.mod go.sum ./

# Copy source code
COPY . .

# Download dependencies and update go.sum
RUN go mod tidy && go mod download

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o meta-core ./cmd/meta-core

# -----------------------------------------------------------------------------
# Stage 2: Build Dashboard
# -----------------------------------------------------------------------------
FROM node:21-alpine AS dashboard-builder
WORKDIR /app
COPY dashboard/package*.json ./
RUN npm install
COPY dashboard/ .
RUN npm run build

# -----------------------------------------------------------------------------
# Stage 3: Build Metadata Editor (standalone)
# -----------------------------------------------------------------------------
FROM node:21-alpine AS editor-builder
WORKDIR /app
COPY editor/package*.json ./
RUN npm install
COPY editor/ .
RUN npm run build

# -----------------------------------------------------------------------------
# Stage 4: Final runtime image
# -----------------------------------------------------------------------------
FROM alpine:3.19

# Container registry metadata
LABEL org.opencontainers.image.source=https://github.com/worph/meta-core
LABEL org.opencontainers.image.description="MetaMesh meta-core - centralized data layer with leader election, Redis, WebDAV, mounts"
LABEL org.opencontainers.image.licenses=MIT

# Pinned rclone release. Alpine's package can lag the latest by several
# minor versions and we depend on VFS cache fixes shipped post-1.65.
ARG RCLONE_VERSION=1.73.5

# Install runtime dependencies. Native NFS/CIFS clients intentionally absent —
# all remote mounts go through rclone now.
RUN apk add --no-cache \
    redis \
    nginx \
    nginx-mod-http-dav-ext \
    supervisor \
    fuse3 \
    bash \
    curl \
    python3 \
    ca-certificates \
    apache2-utils \
    procps

# Install rclone from the official binary release (pinned).
RUN set -eux; \
    apk add --no-cache --virtual .rclone-deps unzip; \
    cd /tmp; \
    curl -fsSL "https://downloads.rclone.org/v${RCLONE_VERSION}/rclone-v${RCLONE_VERSION}-linux-amd64.zip" -o rclone.zip; \
    unzip -q rclone.zip; \
    install -m 0755 "rclone-v${RCLONE_VERSION}-linux-amd64/rclone" /usr/local/bin/rclone; \
    rm -rf rclone.zip "rclone-v${RCLONE_VERSION}-linux-amd64"; \
    apk del .rclone-deps; \
    /usr/local/bin/rclone version | head -2

# Copy Go binary from builder
COPY --from=go-builder /build/meta-core /usr/local/bin/meta-core
RUN chmod +x /usr/local/bin/meta-core

# Create directories
RUN mkdir -p \
    /meta-core/locks \
    /meta-core/db/redis \
    /meta-core/services \
    /meta-core/mounts/errors \
    /files \
    /app/dashboard/dist \
    /app/editor/dist \
    /var/log/supervisor \
    /var/run/supervisor \
    /etc/nginx/ssl

# Copy configuration files
COPY docker/supervisord.conf /etc/supervisor/conf.d/supervisord.conf
COPY docker/nginx.conf /etc/nginx/nginx.conf
COPY docker/mount-watcher.sh /app/scripts/mount-watcher.sh
COPY docker/leader-election.sh /app/scripts/leader-election.sh
COPY docker/entrypoint.sh /entrypoint.sh

# Copy dashboard static files from builder
COPY --from=dashboard-builder /app/dist /app/dashboard/dist

# Copy editor static files from builder
COPY --from=editor-builder /app/dist /app/editor/dist

# Set permissions
RUN chmod +x /app/scripts/mount-watcher.sh /app/scripts/leader-election.sh /entrypoint.sh

# Create htpasswd for rclone basic auth (admin:admin)
RUN htpasswd -bc /etc/nginx/.htpasswd admin admin

# Environment defaults
ENV META_CORE_PATH=/meta-core \
    FILES_PATH=/files \
    SERVICE_NAME=meta-core \
    SERVICE_VERSION=1.0.0 \
    META_CORE_HTTP_PORT=9000 \
    META_CORE_HTTP_HOST=0.0.0.0 \
    REDIS_PORT=6379 \
    WATCH_INTERVAL_MS=1000

# Expose ports
# 80: nginx (dashboard + API proxy + WebDAV)
# 6379: Redis (for other services)
EXPOSE 80 6379

# Health check
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/app/scripts/leader-election.sh"]
