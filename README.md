# meta-core

A Go-based sidecar binary that provides unified leader election, Redis management, and HTTP API for MetaMesh services.

## Overview

meta-core runs alongside each MetaMesh service (meta-sort, meta-fuse, meta-stremio) as a sidecar process managed by supervisord. It provides the foundational infrastructure that enables MetaMesh's distributed architecture without requiring external coordination services like etcd or Consul.

### Key Capabilities

| Feature | Description |
|---------|-------------|
| **Leader Election** | flock-based leader election (Go binary only runs as leader) |
| **Redis Management** | Leader spawns Redis with AOF+RDB persistence, auto-restart on crash |
| **HTTP API** | Language-agnostic REST interface for metadata and service discovery |
| **Service Discovery** | File-based registry with heartbeats and stale detection |
| **Metadata Storage** | Flat key-value schema with connection pooling and batch operations |
| **File Watching** | Directory scanning, MidHash256 computation, event dispatch |
| **Mount Management** | SMB/rclone configuration, health monitoring |
| **WebDAV Server** | File access with nginx proxy_cache |
| **Event Publishing** | Redis keyspace notifications to meta:events stream |

### Design Characteristics

- **No external dependencies** - Uses filesystem locks (works on NFS/CIFS shared volumes)
- **Automatic failover** - Lock releases on process death, followers detect and re-elect
- **Thread-safe** - RWMutex protection on all shared state
- **Graceful shutdown** - Signal handling with reverse startup order
- **Static binary** - CGO disabled, single binary deployment

## Architecture

meta-core runs as a **standalone container** that other services connect to via HTTP API:

```
┌───────────────────────────────────────────────────────────────────────┐
│                    meta-core container (standalone)                    │
│                                                                        │
│  ┌──────────────┐  ┌─────────┐  ┌──────────┐  ┌────────────────────┐  │
│  │ Role         │  │ Redis   │  │ WebDAV   │  │ HTTP API (:9000)   │  │
│  │ Provider     │  │ Server  │  │ Server   │  │ /meta, /urls,      │  │
│  │              │  │         │  │          │  │ /health, /services │  │
│  └──────────────┘  └─────────┘  └──────────┘  └────────────────────┘  │
│                                                                        │
│  Writes: /meta-core/locks/kv-leader.info (URLs for all services)      │
└──────────────────────────────────────────────┬────────────────────────┘
                                               │
        ┌──────────────────────────────────────┴──────────────────────────┐
        │                    /meta-core (shared volume)                    │
        │  ├── locks/kv-leader.info  (leader URLs)                        │
        │  ├── db/redis/             (Redis data)                         │
        │  ├── services/             (hostname-based .json)               │
        │  └── mounts/               (mount configurations)               │
        └─────────────────────────────────────────────────────────────────┘
                 ▲                    ▲                    ▲
    ┌────────────┘                    │                    └────────────┐
    │                                 │                                 │
┌───────────────┐          ┌───────────────┐          ┌───────────────┐
│  meta-sort    │          │  meta-fuse    │          │ meta-stremio  │
│  (reads info, │          │  (reads info, │          │  (reads info, │
│  calls HTTP)  │          │  calls HTTP)  │          │  calls HTTP)  │
└───────────────┘          └───────────────┘          └───────────────┘
```

## Quick Start

### Build

```bash
# Build binary
make build

# Build Docker image
make docker

# Run tests
make test
```

### Run

```bash
# With environment variables
META_CORE_PATH=/meta-core \
FILES_PATH=/files \
SERVICE_NAME=meta-sort \
META_CORE_HTTP_PORT=9000 \
./bin/meta-core
```

### Docker

```bash
docker build -t meta-core .
docker run -v meta-core:/meta-core -v files:/files meta-core
```

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `META_CORE_PATH` | `/meta-core` | Shared volume for locks/db |
| `FILES_PATH` | `/files` | Shared volume for media files |
| `SERVICE_NAME` | `meta-core` | Service identifier |
| `SERVICE_VERSION` | `1.0.0` | Service version |
| `API_PORT` | `8180` | Main service HTTP port |
| `BASE_URL` | - | Stable service URL |
| `REDIS_PORT` | `6379` | Redis port (leader only) |
| `META_CORE_HTTP_PORT` | `9000` | HTTP API port |
| `META_CORE_HTTP_HOST` | `127.0.0.1` | HTTP API bind address |
| `HEALTH_CHECK_INTERVAL_MS` | `5000` | Health check interval |
| `HEARTBEAT_INTERVAL_MS` | `30000` | Service heartbeat interval |
| `STALE_THRESHOLD_MS` | `60000` | Stale service threshold |
| `ENABLE_FILE_WATCHER` | `true` | Enable file scanning |

## API Reference

### Health & Status

```bash
# Health check
curl http://localhost:9000/health
# {"status":"ok","redis":true,"timestamp":"..."}

# Detailed status
curl http://localhost:9000/status
# {"status":"ok","serviceName":"meta-core","version":"1.0.0",...}

# Current leader
curl http://localhost:9000/leader
# {"host":"meta-sort-dev","api":"redis://10.0.1.50:6379","http":"http://10.0.1.50:8180",...}
```

### Metadata Operations

```bash
# List all file hashes
curl http://localhost:9000/meta
# {"hashIds":["midhash256:abc123",...],"count":42}

# Get metadata for a file
curl http://localhost:9000/meta/{hash}
# {"hashId":"midhash256:abc123","metadata":{"title":"Movie","year":"2024",...}}

# Update metadata
curl -X PUT http://localhost:9000/meta/{hash} \
  -H "Content-Type: application/json" \
  -d '{"title":"New Title"}'

# Delete metadata
curl -X DELETE http://localhost:9000/meta/{hash}
```

### Data Operations

```bash
# Get file path
curl http://localhost:9000/data/{hash}/path
# {"hashId":"midhash256:abc123","path":"/files/movies/Movie.mkv","exists":true}

# Check if file exists
curl -I http://localhost:9000/data/{hash}
# HTTP/1.1 200 OK (or 404)
```

### File Operations (by CID)

```bash
# Serve file by CID (looks up poster/backdrop CIDs in metadata)
curl http://localhost:9000/file/{cid} --output poster.jpg
# Returns raw file bytes with appropriate Content-Type header

# Example: fetch a poster image
curl -v http://localhost:9000/file/bafkreih5aznjvttude6c3wbvqeebb6rlx5wkbzyppv7garber7ndsuxku4
# Content-Type: image/jpeg
# [binary image data]
```

The `/file/{cid}` endpoint searches all file metadata for matching `poster` or `backdrop` CIDs and serves the corresponding file from disk. Supports range requests for partial content retrieval.

### Service Discovery

```bash
# List all services
curl http://localhost:9000/services
# {"services":[{"name":"meta-sort",...}],"count":3}

# Get specific service
curl http://localhost:9000/services/meta-sort
# {"name":"meta-sort","version":"2.0.0","api":"http://...","status":"running",...}
```

### Mount Management

```bash
# List all mounts
curl http://localhost:9000/api/mounts/list
# {"mounts":[{"id":"smb-nas","type":"smb","path":"/files/corn/nas",...}]}

# Add a new mount
curl -X POST http://localhost:9000/api/mounts/add \
  -H "Content-Type: application/json" \
  -d '{"type":"smb","path":"/files/corn/nas","config":{...}}'

# Remove a mount
curl -X DELETE http://localhost:9000/api/mounts/smb-nas
```

### File Watching

```bash
# Get scan status
curl http://localhost:9000/api/scan/status
# {"scanning":false,"lastScan":"2024-01-15T10:30:00Z","filesFound":12847}

# Trigger rescan
curl -X POST http://localhost:9000/api/scan/trigger

# Stream file events (SSE)
curl http://localhost:9000/api/events/stream
# data: {"type":"add","path":"movies/Movie.mkv","midhash256":"bafkr..."}
```

### KV Browser

```bash
# Storage statistics
curl http://localhost:9000/api/kv/info
# {"totalKeys":50000,"memoryUsed":"256MB",...}

# List keys with cursor pagination
curl "http://localhost:9000/api/kv/keys?cursor=0&count=100"

# Get specific key value
curl http://localhost:9000/api/kv/key/file:bafkr.../title
```

### Metadata Editor

```bash
# Get all hash IDs
curl http://localhost:9000/api/metadata/hash-ids
# {"hashIds":["bafkr...","bafks..."]}

# Paginated metadata listing
curl "http://localhost:9000/api/metadata/list?page=0&pageSize=50"

# Search metadata
curl -X POST http://localhost:9000/api/metadata/search \
  -H "Content-Type: application/json" \
  -d '{"query":"inception","fields":["title"]}'

# Batch update
curl -X POST http://localhost:9000/api/metadata/batch \
  -H "Content-Type: application/json" \
  -d '{"operations":[{"hashId":"bafkr...","updates":{"title":"New Title"}}]}'

# Clear all metadata
curl -X POST http://localhost:9000/api/metadata/clear
```

### WebDAV Access

```bash
# List directory (returns JSON)
curl http://localhost:9000/webdav/
# [{"name":"watch","isDir":true},{"name":"plugin","isDir":true}]

# Get file
curl http://localhost:9000/webdav/watch/movie.mkv --output movie.mkv

# Upload file
curl -X PUT http://localhost:9000/webdav/plugin/output.txt \
  -H "Content-Type: application/octet-stream" \
  --data-binary @output.txt

# Create directory
curl -X MKCOL http://localhost:9000/webdav/newdir/

# Delete file/directory
curl -X DELETE http://localhost:9000/webdav/oldfile.txt
```

## Leader Election

meta-core uses POSIX file locking (flock) for distributed leader election:

1. Each instance tries to acquire exclusive lock on `/meta-core/locks/kv-leader.lock`
2. Winner becomes **leader**, spawns Redis, writes info to `/meta-core/locks/kv-leader.info`
3. Losers become **followers**, read leader info, connect to leader's Redis
4. Lock automatically releases when process dies (no stale locks)

### Election Flow

```
Instance 1              Instance 2              Instance 3
     │                       │                       │
     ├── Try flock ──────────┼───────────────────────┤
     │                       │                       │
   [WIN]                  [LOSE]                  [LOSE]
   LEADER                 FOLLOWER                FOLLOWER
     │                       │                       │
  Start Redis          Read leader.info       Read leader.info
     │                       │                       │
  Write leader.info    Connect to Redis      Connect to Redis
     │                       │                       │
  [Health loop 5s]     [Health loop 5s]      [Health loop 5s]
  - Update timestamp   - Re-read leader      - Re-read leader
  - Check Redis alive  - Detect changes      - Detect changes
```

### Role Callbacks

The election system provides callbacks for role transitions:

```go
election.OnBecomeLeader(func(info LeaderLockInfo) {
    // Start Redis, initialize resources
})
election.OnBecomeFollower(func(info LeaderLockInfo) {
    // Connect to leader's Redis
})
election.OnLeaderLost(func() {
    // Handle leader failure, attempt re-election
})
```

### Lock Info Format

The `kv-leader.info` file contains a plain text API URL:

```
http://10.0.1.50:9000
```

Services read this file to discover the leader, then call the `/urls` API endpoint to get full connection details (Redis URL, WebDAV URL, base URL, etc.).

## Redis Management

The leader is responsible for spawning and managing the Redis server.

### Redis Configuration

```bash
redis-server --port 6379 --bind 0.0.0.0 \
  --dir /meta-core/db/redis \
  --appendonly yes --appendfilename appendonly.aof \
  --dbfilename dump.rdb --save 60 1 \
  --loglevel warning
```

### Persistence Strategy

| Method | Configuration | Purpose |
|--------|--------------|---------|
| **AOF** | `appendonly yes` | Write-ahead log for durability |
| **RDB** | `save 60 1` | Snapshot after 60s if ≥1 key changed |

### Failure Handling

- **Redis crash**: Auto-detected via health check, automatically restarted
- **Leader crash**: Lock released, followers detect via stale timestamp, re-election occurs
- **Graceful shutdown**: SIGTERM sent to Redis (10s timeout), then SIGKILL if needed

## Metadata Storage

meta-core uses a flat key-value schema in Redis for storing file metadata.

### Storage Format

```
Key: /file/{hashId}/{property}
Value: string

Examples:
/file/midhash256:abc123/title     → "Movie Title"
/file/midhash256:abc123/year      → "2024"
/file/midhash256:abc123/genres    → "action,drama"
/file/midhash256:abc123/filePath  → "movies/Movie.mkv"
```

### Storage Client Features

| Feature | Details |
|---------|---------|
| Connection Pool | 10 connections |
| Timeouts | 5s dial, 30s read/write |
| Batch Operations | Pipelined writes for performance |
| Key Scanning | SCAN-based iteration (non-blocking) |
| CID Lookup | Find file paths by poster/backdrop CID |

## Service Discovery

Services register by writing JSON to `/meta-core/services/{name}.json`.

### Registration Format

```json
{
  "name": "meta-sort",
  "version": "2.0.0",
  "api": "http://10.0.1.50:8180",
  "status": "running",
  "pid": 12345,
  "hostname": "meta-sort-dev",
  "startedAt": "2024-01-01T00:00:00Z",
  "lastHeartbeat": "2024-01-01T00:01:00Z",
  "capabilities": ["meta-core"],
  "endpoints": {
    "health": "http://10.0.1.50:9000/health",
    "meta": "http://10.0.1.50:9000/meta"
  }
}
```

### Heartbeat & Stale Detection

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `HEARTBEAT_INTERVAL_MS` | 30000 | How often services update `lastHeartbeat` |
| `STALE_THRESHOLD_MS` | 60000 | Mark service "stale" if heartbeat older than this |

Services are automatically marked as `stale` when discovered if their `lastHeartbeat` exceeds the threshold. On graceful shutdown, services remove their registration file.

## Development

```bash
# Format code
make fmt

# Run linter
make lint

# Run tests with coverage
make test-cover

# Clean build artifacts
make clean
```

## Internal Packages

| Package | Purpose |
|---------|---------|
| `cmd/meta-core` | Main entry point, startup/shutdown orchestration |
| `internal/config` | Environment-based configuration with defaults |
| `internal/leader` | Election system (`election.go`) and Redis manager (`redis.go`) |
| `internal/storage` | Redis client wrapper with flat key-value operations |
| `internal/discovery` | Service registration, heartbeat loop, discovery |
| `internal/api` | HTTP server, router, and all endpoint handlers |
| `internal/events` | Redis keyspace notification publisher to meta:events |
| `internal/watcher` | File system scanning, MidHash256 computation, event dispatch |
| `internal/watchers` | Polling-based watcher configuration management |
| `internal/mounts` | SMB/rclone mount configuration and health monitoring |
| `internal/webdav` | WebDAV protocol handler with caching integration |

### Startup Sequence

```
1. Load configuration from environment
2. Create storage client (Redis wrapper)
3. Initialize leader election
4. Register role transition callbacks
5. Start election (acquire lock or become follower)
6. Start service discovery (register + heartbeat loop)
7. Initialize dead service cleaner
8. Initialize file watcher (if enabled)
9. Initialize mount manager
10. Start HTTP API server (WebDAV caching handled by nginx)
11. Wait for SIGINT/SIGTERM
12. Shutdown in reverse order
```

## Integration

### As Sidecar in Docker

Add to existing service's supervisord config:

```ini
[program:meta-core]
command=/usr/local/bin/meta-core
priority=10
autostart=true
autorestart=true
```

### Client Usage (TypeScript)

```typescript
const response = await fetch('http://localhost:9000/meta/' + hashId);
const { metadata } = await response.json();
```

### Client Usage (Python)

```python
import requests
response = requests.get(f'http://localhost:9000/meta/{hash_id}')
metadata = response.json()['metadata']
```

## Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| `github.com/gorilla/mux` | v1.8.1 | HTTP router |
| `github.com/redis/go-redis/v9` | v9.4.0 | Redis client |
| `golang.org/x/net` | v0.20.0 | WebDAV support |
| `github.com/google/uuid` | v1.6.0 | UUID generation |
| Go stdlib | - | context, encoding/json, net, os, sync, syscall |

### Runtime Requirements

- **Go 1.21+** for building
- **Redis** binary available in PATH (leader spawns it)
- **Shared filesystem** accessible by all instances (for flock)

## License

Part of the MetaMesh project.
