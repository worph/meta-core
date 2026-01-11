# meta-core

A Go-based sidecar binary that provides unified leader election, Redis management, and HTTP API for MetaMesh services.

## Overview

meta-core runs alongside each MetaMesh service (meta-sort, meta-fuse, meta-stremio) as a sidecar process managed by supervisord. It provides the foundational infrastructure that enables MetaMesh's distributed architecture without requiring external coordination services like etcd or Consul.

### Key Capabilities

| Feature | Description |
|---------|-------------|
| **Leader Election** | POSIX flock-based distributed consensus on shared filesystem |
| **Redis Management** | Leader spawns Redis with AOF+RDB persistence, auto-restart on crash |
| **HTTP API** | Language-agnostic REST interface for metadata and service discovery |
| **Service Discovery** | File-based registry with heartbeats and stale detection |
| **Metadata Storage** | Flat key-value schema with connection pooling and batch operations |

### Design Characteristics

- **No external dependencies** - Uses filesystem locks (works on NFS/CIFS shared volumes)
- **Automatic failover** - Lock releases on process death, followers detect and re-elect
- **Thread-safe** - RWMutex protection on all shared state
- **Graceful shutdown** - Signal handling with reverse startup order
- **Static binary** - CGO disabled, single binary deployment

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Container (meta-sort / meta-fuse / meta-stremio)          │
│                                                             │
│  ┌──────────────┐      HTTP API      ┌──────────────────┐  │
│  │ Main Service │ ◄────────────────► │   meta-core      │  │
│  │ (TS/Python)  │    localhost:9000  │   (Go binary)    │  │
│  └──────────────┘                    └────────┬─────────┘  │
│                                               │             │
└───────────────────────────────────────────────┼─────────────┘
                                                │
                        ┌───────────────────────┴───────────────┐
                        │  /meta-core (shared volume)           │
                        │  ├── locks/kv-leader.lock             │
                        │  ├── locks/kv-leader.info             │
                        │  ├── db/redis/                        │
                        │  └── services/*.json                  │
                        └───────────────────────────────────────┘
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

## API Reference

### Health & Status

```bash
# Health check
curl http://localhost:9000/health
# {"status":"ok","role":"leader","redis":true,"timestamp":"..."}

# Detailed status
curl http://localhost:9000/status
# {"status":"ok","role":"leader","serviceName":"meta-core","version":"1.0.0",...}

# Current leader
curl http://localhost:9000/leader
# {"host":"meta-sort-dev","api":"redis://10.0.1.50:6379","http":"http://10.0.1.50:8180",...}

# This instance's role
curl http://localhost:9000/role
# {"role":"leader"}
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

```json
{
  "host": "meta-sort-dev",
  "api": "redis://10.0.1.50:6379",
  "http": "http://10.0.1.50:8180",
  "baseUrl": "http://localhost:8180",
  "timestamp": 1704067200000,
  "pid": 12345
}
```

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

### Startup Sequence

```
1. Load configuration from environment
2. Create storage client (Redis wrapper)
3. Initialize leader election
4. Register role transition callbacks
5. Start election (acquire lock or become follower)
6. Start service discovery (register + heartbeat loop)
7. Start HTTP API server
8. Wait for SIGINT/SIGTERM
9. Shutdown in reverse order
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
| Go stdlib | - | context, encoding/json, net, os, sync, syscall |

### Runtime Requirements

- **Go 1.21+** for building
- **Redis** binary available in PATH (leader spawns it)
- **Shared filesystem** accessible by all instances (for flock)

## License

Part of the MetaMesh project.
