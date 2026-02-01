# Meta-Core Architecture

## Document Purpose

This document describes the architectural design of **meta-core**, the unified data and metadata access layer for MetaMesh. Meta-core is a language-agnostic sidecar binary that runs alongside each service container, providing KV storage, leader election, service discovery, and file access APIs. This document covers the sidecar pattern, leader election mechanism, API design, data model, and integration patterns.

---

## System Overview

MetaMesh consists of three services that require shared access to metadata and files:

| Service | Language | Role |
|---------|----------|------|
| **meta-sort** | TypeScript | Watches folders, extracts metadata, writes to KV |
| **meta-fuse** | TypeScript/Rust | Serves virtual filesystem from KV metadata |
| **meta-stremio** | Python | Stremio addon for HLS streaming |

### Problem Statement

Without meta-core, each service implements its own:
- Leader election logic (616 lines TS + 301 lines Python + 277 lines TS)
- Redis client configuration
- Service discovery mechanism
- File path resolution

This leads to:
1. **Code duplication** across languages
2. **Inconsistent behavior** between implementations
3. **Tight coupling** to Redis internals
4. **Complex maintenance** when adding new services

### Core Design Principle

**Provide a single, language-agnostic interface for all data and metadata operations, abstracting storage implementation from service logic.**

This design enables:
- **Language independence**: Any service language can use HTTP API
- **Single source of truth**: One leader manages all storage
- **Clean abstraction**: Services don't know about Redis, flock, or file paths
- **Operational simplicity**: One binary to deploy, monitor, and debug

---

## Architecture Design

### Sidecar Pattern

Meta-core runs as a **sidecar process** in each container, alongside the main service:

```
┌─────────────────────────────────────────────────────────────────┐
│  Container (meta-sort / meta-fuse / meta-stremio)               │
│                                                                 │
│  ┌──────────────────┐    HTTP API      ┌──────────────────────┐│
│  │   Main Service   │◄────────────────►│     meta-core        ││
│  │                  │  localhost:9000  │     (Go binary)      ││
│  │  - TypeScript    │                  │                      ││
│  │  - Python        │                  │  - Leader election   ││
│  │  - Any language  │                  │  - Redis management  ││
│  │                  │                  │  - Service discovery ││
│  └──────────────────┘                  │  - File access API   ││
│                                        └──────────┬───────────┘│
│                                                   │             │
└───────────────────────────────────────────────────┼─────────────┘
                                                    │
                    ┌───────────────────────────────┼───────────────┐
                    │     /meta-core (shared volume)                │
                    │                               ▼               │
                    │  ┌─────────────────────────────────────────┐ │
                    │  │  locks/                                 │ │
                    │  │    kv-leader.lock    (flock marker)     │ │
                    │  │    kv-leader.info    (leader JSON)      │ │
                    │  │  db/                                    │ │
                    │  │    redis/            (RDB + AOF)        │ │
                    │  │  services/                              │ │
                    │  │    meta-sort.json    (service registry) │ │
                    │  │    meta-fuse.json                       │ │
                    │  │    meta-stremio.json                    │ │
                    │  └─────────────────────────────────────────┘ │
                    └──────────────────────────────────────────────┘

                    ┌──────────────────────────────────────────────┐
                    │     /files (shared volume)                   │
                    │                                              │
                    │  media/                                      │
                    │    movies/                                   │
                    │      Movie.2024.mkv                          │
                    │    series/                                   │
                    │      Show.S01E01.mkv                         │
                    └──────────────────────────────────────────────┘
```

### Multi-Container Topology

Across all containers, exactly **one meta-core instance** is the leader:

```
┌─────────────────────┐  ┌─────────────────────┐  ┌─────────────────────┐
│  meta-sort          │  │  meta-fuse          │  │  meta-stremio       │
│  container          │  │  container          │  │  container          │
│                     │  │                     │  │                     │
│  ┌───────────────┐  │  │  ┌───────────────┐  │  │  ┌───────────────┐  │
│  │  meta-core    │  │  │  │  meta-core    │  │  │  │  meta-core    │  │
│  │  ★ LEADER     │  │  │  │  follower     │  │  │  │  follower     │  │
│  │               │  │  │  │               │  │  │  │               │  │
│  │  [Redis]──────┼──┼──┼──┼───────────────┼──┼──┼──┼───────────────│  │
│  │               │  │  │  │               │  │  │  │               │  │
│  └───────────────┘  │  │  └───────────────┘  │  │  └───────────────┘  │
│         ▲           │  │         │           │  │         │           │
│         │ HTTP      │  │         │ HTTP      │  │         │ HTTP      │
│         ▼           │  │         ▼           │  │         ▼           │
│  ┌───────────────┐  │  │  ┌───────────────┐  │  │  ┌───────────────┐  │
│  │  meta-sort    │  │  │  │  meta-fuse    │  │  │  │  meta-stremio │  │
│  │  service      │  │  │  │  service      │  │  │  │  service      │  │
│  └───────────────┘  │  │  └───────────────┘  │  │  └───────────────┘  │
└─────────────────────┘  └─────────────────────┘  └─────────────────────┘
          │                        │                        │
          └────────────────────────┼────────────────────────┘
                                   ▼
                    ┌──────────────────────────────┐
                    │  /meta-core (shared volume)  │
                    │  /files (shared volume)      │
                    └──────────────────────────────┘
```

---

## Leader Election

### Mechanism: flock-based Consensus

Meta-core uses **POSIX file locking (flock)** on a shared filesystem for leader election. This approach requires no external consensus system (etcd, Consul, Zookeeper).

```
Leader Election Flow:

    meta-core startup
           │
           ▼
    ┌──────────────────┐
    │ Try flock() on   │
    │ kv-leader.lock   │
    │ (non-blocking)   │
    └────────┬─────────┘
             │
     ┌───────┴───────┐
     │               │
  acquired        blocked
     │               │
     ▼               ▼
┌─────────┐    ┌──────────┐
│ LEADER  │    │ FOLLOWER │
└────┬────┘    └────┬─────┘
     │              │
     ▼              ▼
┌──────────┐  ┌────────────┐
│ Spawn    │  │ Read       │
│ Redis    │  │ leader.info│
│          │  │            │
│ Write    │  │ Connect to │
│ leader   │  │ leader     │
│ .info    │  │ Redis      │
└──────────┘  └────────────┘
```

### Lock File Structure

**`/meta-core/locks/kv-leader.lock`**
- Binary flock marker (empty file)
- Held exclusively by leader process
- Automatically released on process death (kernel handles cleanup)

**`/meta-core/locks/kv-leader.info`**
```json
{
  "hostname": "meta-sort-dev",
  "redis_url": "redis://10.0.1.50:6379",
  "http_url": "http://10.0.1.50:9000",
  "started_at": "2024-01-15T10:30:00Z",
  "pid": 12345,
  "version": "1.0.0"
}
```

### Failover Behavior

When the leader process dies:

1. **Kernel releases flock** automatically (no stale lock possible)
2. **Followers detect** via:
   - File watch on `kv-leader.info`
   - Health check failure to leader HTTP endpoint
3. **First follower acquires lock** becomes new leader
4. **New leader spawns Redis** using existing data in `/meta-core/db/redis/`
5. **Other followers reconnect** to new leader

```
Timeline: Leader Failure and Recovery

t=0     t=1        t=2         t=3         t=4
 │       │          │           │           │
 ▼       ▼          ▼           ▼           ▼
Leader  Leader    Flock      Follower   New leader
running crashes   released   acquires   spawns Redis
                             flock      & serves
```

### Leader Responsibilities

The leader meta-core instance:

1. **Spawns Redis** as a child process on port 6379
2. **Writes leader.info** with connection details
3. **Accepts all write operations** (metadata updates)
4. **Serves read operations** (metadata queries, file paths)
5. **Manages service registry** (heartbeats, stale detection)

### Follower Responsibilities

Follower meta-core instances:

1. **Monitor leader.info** for leader changes
2. **Proxy requests to leader** for writes
3. **Cache metadata locally** for fast reads (optional)
4. **Attempt leader acquisition** on leader failure

---

## Data Model

### Two-Concept Model

Meta-core manages two distinct but related concepts:

```
┌─────────────────────────────────────────────────────────────────┐
│                         METADATA (KV Store)                     │
│                                                                 │
│  Key: midhash256 CID                                           │
│  Value: {                                                       │
│    "title": "Movie Name",                                       │
│    "year": 2024,                                                │
│    "path": "movies/Movie.2024.mkv",   ◄── relative to /files   │
│    "size": 15032385536,                                         │
│    "duration": 7200,                                            │
│    "codec": "hevc",                                             │
│    ...                                                          │
│  }                                                              │
│                                                                 │
│  ✓ Can exist WITHOUT local file (remote/deleted)               │
│  ✓ Searchable by any field                                      │
│  ✓ Stored in Redis hashes                                       │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                           DATA (Files)                          │
│                                                                 │
│  Location: /files volume (shared across containers)            │
│  Access: Via path from metadata                                 │
│                                                                 │
│  ✓ Always has metadata (at minimum: hash, path, title)         │
│  ✓ Accessed by path or streamed via API                        │
│  ✓ Never duplicated or moved                                    │
└─────────────────────────────────────────────────────────────────┘
```

### Path Resolution

File paths in metadata are **relative to the /files volume**:

```
Metadata path:  "movies/Movie.2024.mkv"
Container path: "/files/movies/Movie.2024.mkv"
Host path:      "${DATA_WATCH_PATH}/movies/Movie.2024.mkv"
```

This enables:
- **Multi-host deployments**: Same metadata works across machines
- **Volume remapping**: Change mount points without updating metadata
- **Portable backups**: Metadata export/import works across systems

---

## API Design

### Endpoint Overview

Meta-core exposes an HTTP API on `localhost:9000` (configurable):

```
Health & Status
  GET  /health                    → Health check and role info
  GET  /status                    → Detailed status and metrics

Metadata Operations
  GET  /meta/{hash}               → Get metadata by hash
  PUT  /meta/{hash}               → Create/update metadata
  DELETE /meta/{hash}             → Delete metadata
  GET  /meta?search=...           → Search metadata

Data Operations
  GET  /data/{hash}/path          → Get file path
  GET  /data/{hash}/stream        → Stream file content
  HEAD /data/{hash}               → Check if file exists

Service Discovery
  GET  /services                  → List registered services
  POST /services/{name}           → Register/heartbeat service
  DELETE /services/{name}         → Deregister service
```

### Health Endpoint

```
GET /health

Response:
{
  "status": "healthy",
  "role": "leader",              // or "follower"
  "leader": {
    "hostname": "meta-sort-dev",
    "http_url": "http://10.0.1.50:9000",
    "redis_url": "redis://10.0.1.50:6379"
  },
  "uptime_seconds": 3600,
  "version": "1.0.0"
}
```

### Metadata CRUD

**Get Metadata**
```
GET /meta/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku

Response:
{
  "hash": "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku",
  "path": "movies/Inception.2010.mkv",
  "title": "Inception",
  "year": 2010,
  "duration": 8880,
  "size": 15032385536,
  ...
}
```

**Update Metadata**
```
PUT /meta/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku
Content-Type: application/json

{
  "title": "Inception",
  "year": 2010,
  "custom.rating": 9.5
}

Response:
{
  "success": true,
  "hash": "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
}
```

**Search Metadata**
```
GET /meta?title=inception&year=2010&limit=10&offset=0

Response:
{
  "results": [
    { "hash": "bafkrei...", "title": "Inception", ... }
  ],
  "total": 1,
  "limit": 10,
  "offset": 0
}
```

### Data Access

**Get File Path**
```
GET /data/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku/path

Response:
{
  "path": "/files/movies/Inception.2010.mkv",
  "exists": true,
  "size": 15032385536
}
```

**Stream File Content**
```
GET /data/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku/stream
Range: bytes=0-1048575

Response:
HTTP/1.1 206 Partial Content
Content-Type: application/octet-stream
Content-Range: bytes 0-1048575/15032385536
Content-Length: 1048576

[binary data]
```

### Service Discovery

**Register Service**
```
POST /services/meta-fuse
Content-Type: application/json

{
  "hostname": "meta-fuse-dev",
  "http_url": "http://10.0.1.51:8181",
  "version": "2.0.0",
  "capabilities": ["vfs", "webdav"]
}

Response:
{
  "success": true,
  "ttl_seconds": 60
}
```

**List Services**
```
GET /services

Response:
{
  "services": [
    {
      "name": "meta-sort",
      "hostname": "meta-sort-dev",
      "http_url": "http://10.0.1.50:8180",
      "status": "healthy",
      "last_heartbeat": "2024-01-15T10:30:00Z"
    },
    {
      "name": "meta-fuse",
      "hostname": "meta-fuse-dev",
      "http_url": "http://10.0.1.51:8181",
      "status": "healthy",
      "last_heartbeat": "2024-01-15T10:30:05Z"
    }
  ]
}
```

---

## Implementation

### Technology Choice: Go

Meta-core is implemented in Go for the following reasons:

| Requirement | Go Advantage |
|-------------|--------------|
| Language agnostic | HTTP API, no runtime dependencies |
| Single binary | Static compilation, no interpreter |
| Low resource usage | Small memory footprint (~10-20MB) |
| File locking | stdlib `syscall.Flock` |
| HTTP server | stdlib `net/http` |
| Redis client | go-redis (mature, full-featured) |
| Cross-platform | Compile for Linux, macOS, Windows |

### Binary Structure

```
meta-core
├── cmd/
│   └── meta-core/
│       └── main.go           # Entry point
├── internal/
│   ├── leader/
│   │   ├── election.go       # flock-based election
│   │   └── redis.go          # Redis process management
│   ├── api/
│   │   ├── server.go         # HTTP server setup
│   │   ├── health.go         # Health endpoints
│   │   ├── meta.go           # Metadata endpoints
│   │   ├── data.go           # Data endpoints
│   │   └── services.go       # Service discovery endpoints
│   ├── storage/
│   │   ├── redis.go          # Redis client wrapper
│   │   └── cache.go          # Optional local cache
│   └── config/
│       └── config.go         # Configuration loading
├── go.mod
└── go.sum
```

### Process Management

Meta-core manages the Redis process as a child:

```go
// Leader spawns Redis
cmd := exec.Command("redis-server",
    "--port", "6379",
    "--dir", "/meta-core/db/redis",
    "--save", "60", "1",        // RDB snapshot
    "--appendonly", "yes",      // AOF persistence
)
cmd.Start()

// Monitor Redis health
go func() {
    for {
        if err := redisClient.Ping(); err != nil {
            log.Error("Redis unhealthy, restarting...")
            cmd.Process.Kill()
            cmd.Start()
        }
        time.Sleep(5 * time.Second)
    }
}()
```

### Container Integration

Meta-core runs via supervisord alongside the main service:

```ini
# /etc/supervisor/conf.d/meta-core.conf

[program:meta-core]
command=/usr/local/bin/meta-core
directory=/meta-core
autostart=true
autorestart=true
priority=1
startsecs=2
stdout_logfile=/var/log/meta-core.log
stderr_logfile=/var/log/meta-core.err

[program:main-service]
command=/app/start.sh
autostart=true
autorestart=true
priority=10
startsecs=5
```

### Configuration

Environment variables configure meta-core:

```bash
# Core paths
META_CORE_PATH=/meta-core          # Shared volume for locks/db
FILES_PATH=/files                   # Shared volume for media files

# Network
META_CORE_HTTP_PORT=9000           # HTTP API port
META_CORE_REDIS_PORT=6379          # Redis port (leader only)

# Behavior
META_CORE_SERVICE_NAME=meta-sort   # Service identity for discovery
META_CORE_LEADER_TIMEOUT=30        # Seconds before leader considered dead
META_CORE_HEARTBEAT_INTERVAL=10    # Service heartbeat frequency
```

---

## Client Integration

### TypeScript Client

Services use a simple HTTP client to interact with meta-core:

```typescript
// meta-core-client.ts

class MetaCoreClient {
  private baseUrl = 'http://localhost:9000';

  async getMeta(hash: string): Promise<Metadata | null> {
    const res = await fetch(`${this.baseUrl}/meta/${hash}`);
    if (res.status === 404) return null;
    return res.json();
  }

  async setMeta(hash: string, meta: Partial<Metadata>): Promise<void> {
    await fetch(`${this.baseUrl}/meta/${hash}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(meta),
    });
  }

  async getDataPath(hash: string): Promise<string | null> {
    const res = await fetch(`${this.baseUrl}/data/${hash}/path`);
    if (res.status === 404) return null;
    const data = await res.json();
    return data.path;
  }

  async search(query: SearchQuery): Promise<SearchResult> {
    const params = new URLSearchParams(query as any);
    const res = await fetch(`${this.baseUrl}/meta?${params}`);
    return res.json();
  }

  async isHealthy(): Promise<boolean> {
    try {
      const res = await fetch(`${this.baseUrl}/health`);
      return res.ok;
    } catch {
      return false;
    }
  }
}

export const metaCore = new MetaCoreClient();
```

### Python Client

```python
# meta_core_client.py

import requests
from typing import Optional, Dict, Any

class MetaCoreClient:
    def __init__(self, base_url: str = "http://localhost:9000"):
        self.base_url = base_url

    def get_meta(self, hash: str) -> Optional[Dict[str, Any]]:
        res = requests.get(f"{self.base_url}/meta/{hash}")
        if res.status_code == 404:
            return None
        return res.json()

    def set_meta(self, hash: str, meta: Dict[str, Any]) -> None:
        requests.put(
            f"{self.base_url}/meta/{hash}",
            json=meta
        )

    def get_data_path(self, hash: str) -> Optional[str]:
        res = requests.get(f"{self.base_url}/data/{hash}/path")
        if res.status_code == 404:
            return None
        return res.json()["path"]

    def search(self, **query) -> Dict[str, Any]:
        res = requests.get(f"{self.base_url}/meta", params=query)
        return res.json()

    def is_healthy(self) -> bool:
        try:
            res = requests.get(f"{self.base_url}/health")
            return res.ok
        except:
            return False

meta_core = MetaCoreClient()
```

---

## Search Capabilities

### Current Implementation

Basic search uses Redis SCAN with pattern matching:

```
GET /meta?title=inception

→ SCAN 0 MATCH "file:*" COUNT 100
→ For each key: HGET key title → filter matches
```

This works for small collections but scales poorly.

### Enhanced Search (Future)

For advanced search, meta-core supports Redis Search module:

```
# Create search index
FT.CREATE idx:files ON HASH PREFIX 1 "file:" SCHEMA
  title TEXT WEIGHT 5.0
  series_title TEXT WEIGHT 3.0
  year NUMERIC SORTABLE
  media_type TAG
  duration NUMERIC SORTABLE

# Search query
FT.SEARCH idx:files "@title:inception @year:[2010 2010]"
```

Search query syntax:

```
GET /meta?q=title:inception AND year:2010
GET /meta?q=media_type:episode AND series_title:"Breaking Bad"
GET /meta?q=duration:[3600 7200]   # 1-2 hours
```

---

## Failure Modes

### Leader Crash

1. **Detection**: Followers detect via health check failure (5s timeout)
2. **Election**: Fastest follower acquires flock
3. **Recovery**: New leader spawns Redis with existing data
4. **Reconnection**: Other followers read new leader.info, reconnect
5. **Downtime**: 5-10 seconds typical

### Redis Crash

1. **Detection**: Leader detects ping failure
2. **Recovery**: Leader restarts Redis process
3. **Data**: Recovered from RDB snapshot + AOF replay
4. **Downtime**: 1-2 seconds typical

### Network Partition

1. **Followers isolated**: Continue serving cached reads, writes fail
2. **Leader isolated**: Continues operating, followers elect new leader
3. **Resolution**: On reconnection, followers sync with true leader
4. **Risk**: Brief split-brain if partition during election (mitigated by flock atomicity)

### Shared Volume Unavailable

1. **Impact**: All meta-core instances fail
2. **Behavior**: Services operate in degraded mode (no metadata)
3. **Recovery**: Automatic when volume restored

---

## Observability

### Metrics Endpoint

```
GET /metrics

# Prometheus format
meta_core_role{service="meta-sort"} 1              # 1=leader, 0=follower
meta_core_uptime_seconds{service="meta-sort"} 3600
meta_core_requests_total{endpoint="/meta"} 15420
meta_core_request_duration_seconds{endpoint="/meta",quantile="0.99"} 0.005
meta_core_redis_commands_total 892341
meta_core_files_total 12847
meta_core_metadata_bytes 45678901
```

### Logging

Structured JSON logging:

```json
{
  "time": "2024-01-15T10:30:00Z",
  "level": "info",
  "msg": "Acquired leader lock",
  "service": "meta-sort",
  "pid": 12345
}
```

Log levels: `debug`, `info`, `warn`, `error`

### Health Probes

For Kubernetes/Docker health checks:

```yaml
# docker-compose.yml
healthcheck:
  test: ["CMD", "curl", "-f", "http://localhost:9000/health"]
  interval: 10s
  timeout: 5s
  retries: 3
```

---

## Security Considerations

### Network Isolation

- Meta-core listens on `localhost:9000` only (not exposed externally)
- Redis listens on `localhost:6379` only
- Inter-container communication uses Docker network

### File Access

- Meta-core runs as non-root user
- Read-only access to /files volume (except meta-sort)
- No shell access or command execution

### Input Validation

- Hash parameters validated as valid CID format
- Path traversal prevented (paths must be relative, no `..`)
- JSON body size limited (10MB max)

---

## Configuration Reference

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `META_CORE_PATH` | `/meta-core` | Path to shared infrastructure volume |
| `FILES_PATH` | `/files` | Path to shared files volume |
| `META_CORE_HTTP_PORT` | `9000` | HTTP API port |
| `META_CORE_REDIS_PORT` | `6379` | Redis port (leader only) |
| `META_CORE_SERVICE_NAME` | hostname | Service identity |
| `META_CORE_LEADER_TIMEOUT` | `30` | Leader health timeout (seconds) |
| `META_CORE_HEARTBEAT_INTERVAL` | `10` | Heartbeat frequency (seconds) |
| `META_CORE_LOG_LEVEL` | `info` | Log verbosity |
| `META_CORE_CACHE_ENABLED` | `false` | Enable local read cache |
| `META_CORE_CACHE_TTL` | `60` | Cache TTL (seconds) |

### File Paths

| Path | Purpose |
|------|---------|
| `/meta-core/locks/kv-leader.lock` | Leader election flock file |
| `/meta-core/locks/kv-leader.info` | Leader connection info |
| `/meta-core/db/redis/dump.rdb` | Redis RDB snapshot |
| `/meta-core/db/redis/appendonly.aof` | Redis AOF log |
| `/meta-core/services/*.json` | Service registry files |

---

## Summary

Meta-core provides a unified, language-agnostic interface for MetaMesh's data and metadata operations. By consolidating leader election, storage management, and service discovery into a single sidecar binary, it:

1. **Eliminates code duplication** across TypeScript and Python services
2. **Simplifies service implementation** to pure business logic
3. **Provides operational visibility** through consistent health/metrics
4. **Enables future evolution** by abstracting storage implementation

The flock-based leader election ensures exactly-one-leader semantics without external dependencies, while the HTTP API enables integration from any programming language.
