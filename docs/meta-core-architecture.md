# Meta-Core Architecture

## Document Purpose

This document describes the architectural design of **meta-core**, the
standalone container that owns Redis, the leader-info file, the HTTP+SSE
metadata surface, and a WebDAV file server. It covers the leader gate,
storage data model, API design, the SSE event surface, and the integration
contract with other MetaMesh services.

For the on-disk Redis layout, see
[metadata-storage-structure.md](metadata-storage-structure.md). For the
external-access design (Redis lockdown, SSE wire contract, auth rules),
see [api-mediated-access.md](api-mediated-access.md). For the UUID-rooted
storage rationale, see [uuid-rooted-metadata.md](uuid-rooted-metadata.md).

---

## System Overview

MetaMesh is a collection of services that need a shared, ordered, durable
view of file metadata:

| Service | Language | Role |
|---|---|---|
| **meta-sort** | TypeScript | Watches folders, runs plugins, writes metadata via meta-core's HTTP API. |
| **meta-fuse** | TypeScript / Rust | Serves the virtual filesystem from metadata. |
| **meta-stremio** | Python | Stremio addon for HLS streaming. |
| **meta-dup** | TypeScript | Duplicate detection. |
| **meta-share** | Rust | Decentralised metadata sharing (libp2p / Kamilata). |

meta-core sits below all of these as the single owner of:

1. **Redis** — the metadata store and event source.
2. **The leader-info file** — the one place services look to discover meta-core's URLs.
3. **The HTTP+SSE surface** — the only way external services touch the data plane.
4. **A WebDAV server** — file access for plugins and other services.

### Core design principle

> External services never speak Redis. Every read, write, and event flows
> through meta-core's HTTP+SSE surface.

This is the contract that PR D of [api-mediated-access.md](api-mediated-access.md)
landed and that the rest of this document assumes.

---

## Architecture

### Standalone container topology

meta-core runs as a dedicated container (`metacore-app`). The dev compose
runs a single instance; the legacy multi-instance topology (sibling
followers) was deprecated. The flock primitive is retained as the
correctness anchor — it guarantees that at most one process owns Redis and
the writable state under `/meta-core`.

```
┌────────────────────────────────────────────────────────────────────────┐
│                  metacore-app  (standalone container)                  │
│                                                                        │
│  supervisord                                                           │
│   ├─ leader-election.sh   (flock → write kv-leader.info → exec self)   │
│   ├─ redis-server         (AOF + RDB on /meta-core/db/redis)           │
│   ├─ nginx                (TLS termination, proxy_cache for /webdav)   │
│   ├─ rclone rcd           (mount daemon)                               │
│   ├─ mount-watcher.sh     (rclone mount lifecycle)                     │
│   └─ meta-core (Go) ──►   HTTP + SSE + WebDAV on :9000                 │
└────────────────────────────────────────────────────────────────────────┘
                                    │
       ┌────────────────────────────┴────────────────────────────┐
       │  /meta-core (shared volume)                             │
       │    locks/kv-leader.{lock,info}                          │
       │    db/redis/                                            │
       │    services/*.json                                      │
       │    mounts/{mounts.json, errors/}                        │
       │    watchers.json                                        │
       │    cache/  (nginx proxy_cache)                          │
       └─────────────────────────────────────────────────────────┘
                                    ▲
       ┌────────────────────────────┼────────────────────────────┐
       │                            │                            │
┌─────────────┐             ┌─────────────┐              ┌─────────────┐
│  meta-sort  │ HTTP / SSE  │  meta-fuse  │ HTTP / SSE   │ meta-stremio│
│             ├────────────►│             ├─────────────►│             │
│             │             │             │              │             │
└─────────────┘             └─────────────┘              └─────────────┘
```

### `/files` shared volume

Independently of `/meta-core`, every service mounts the `/files` volume
read-only (write access scoped to specific subtrees: `/files/plugin`,
`/files/corn` mounts). meta-core exposes this volume over WebDAV at
`/webdav/*` so plugins running in their own containers don't have to mount
it themselves.

```
/files
  ├── watch/    (host media, read-only)
  ├── test/     (test fixtures)
  ├── plugin/   (plugin output, read+write)
  └── corn/     (rclone-managed mounts)
```

---

## Leader Gate

### Mechanism: bash + flock

Leader election is implemented in `docker/leader-election.sh`, not in Go.
The script:

1. Opens `${META_CORE_PATH}/locks/kv-leader.lock` on file descriptor 200.
2. Tries `flock -n 200`. On success it transitions to LEADER, writes the
   API URL to `${META_CORE_PATH}/locks/kv-leader.info`, and `exec`s
   supervisord — replacing itself in the process tree. When supervisord
   exits, the kernel releases the flock automatically.
3. On failure (someone else holds the lock) it sleeps `ELECTION_RETRY_SECS`
   (default `5s`) and retries. Followers loop in bash and never start the
   Go binary.

```
Container startup
       │
       ▼
┌──────────────────┐
│ leader-election  │
│ .sh              │
│ flock(kv-leader  │
│  .lock)          │
└────────┬─────────┘
         │
    ┌────┴────┐
    │         │
 acquired  blocked
    │         │
    ▼         ▼
┌─────────┐  ┌──────────────┐
│ Write   │  │ sleep N,     │
│ kv-     │  │ retry        │
│ leader  │  └──────────────┘
│ .info   │
│         │
│ exec    │
│ super-  │
│ visord  │
└─────────┘
       │
       ▼
┌──────────────────────────────┐
│ supervisord starts:          │
│   redis → nginx → rclone →   │
│   meta-core Go binary        │
└──────────────────────────────┘
```

### Implications for the Go binary

The Go binary at `cmd/meta-core/main.go` is invoked only after the bash
gate wins. Therefore:

- `internal/leader/election.go` does **not** run an election loop. It
  contains `LeaderInfoProvider`, which builds the leader info struct from
  local hostname + IP + config and returns it on demand. There are no
  `OnBecomeLeader` / `OnBecomeFollower` / `OnLeaderLost` callbacks; those
  states do not exist inside the Go process.
- `LeaderLockInfo.RedisUrl` is intentionally empty in the response from
  `/urls` — see `internal/leader/election.go:82`. PR D of
  api-mediated-access.md removed direct Redis exposure; consumers route
  metadata I/O through meta-core's HTTP API and SSE event streams.

### Lock-info file

`/meta-core/locks/kv-leader.info` is a single plaintext line:

```
http://10.0.1.50:9000
```

Other services read this file, then call `GET /urls` for full discovery
(hostname, baseUrl, apiUrl, webdavUrl, webdavUrlInternal).

### Failure handling

- **meta-core process dies** → supervisord restarts it. The bash flock
  loop does not need to participate — supervisord owns the lock through
  its lifetime.
- **Container dies** → kernel releases the flock. If a standby
  `metacore-app` is provisioned, its bash loop will acquire on next tick.
  The dev compose runs a single instance; failover is "restart the
  container."
- **Redis crash inside the container** → supervisord restarts redis. The
  Go binary's connect loop retries up to 30 times (1s delay) at startup;
  at steady state, individual operations fail and clients retry.

---

## Schema-Version Gate

Before serving any traffic, `cmd/meta-core/main.go:54-61` calls
`storage.EnsureSchemaVersion`. The sentinel lives in Redis at
`meta-core:schema-version` (`internal/storage/schema_version.go`). The
gate refuses to boot when the existing Redis layout predates the
UUID-rooted schema, so legacy `file:midhash256:*` data can't leak into
the current code paths. In alpha there is no automated migration —
operators wipe with `pnpm run clean:all` and restart.

---

## Data Model

### Two concepts: metadata vs. file bytes

```
┌────────────────────────────────────────────────────────────────────┐
│                       METADATA  (Redis)                            │
│                                                                    │
│  Root: opaque UUIDv7 (Crockford Base32; ULID layout, 26 chars)     │
│  Storage: flat string keys                                         │
│           file:<uuid>/<property>           → string value          │
│           file:<uuid>/cids                 → SET of CID tokens     │
│           file:<uuid>/canonical_cid        → preferred CID         │
│           file:<uuid>/duplicates           → SET of paths          │
│                                                                    │
│  Aliases (reverse index, one Redis STRING each):                   │
│           cid:<algorithm>:<value>          → <uuid>                │
│                                                                    │
│  Index:   file:__index__                   → SET of all UUIDs      │
│                                                                    │
│  Mintable without a local file (remote / deleted entries OK).      │
│  Resolvable by any registered CID in O(1).                         │
└────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────┐
│                         FILE BYTES                                 │
│                                                                    │
│  Location: /files volume (shared across containers).               │
│  Access:   path resolution via metadata, served via /webdav/*,    │
│            /file/{cid} (reverse-lookup), or /data/{hash}.          │
│  Path stored as filePath in the metadata, relative to /files.      │
└────────────────────────────────────────────────────────────────────┘
```

### Why UUID-rooted?

Roots were previously content-hash tokens (`file:midhash256:abc/...`),
which privileged one hash algorithm. UUID-rooted storage treats every CID
symmetrically as an alias, makes `GET /api/meta/<cid>` O(1) for any
registered CID, and lets new digest types be added without rewriting root
keys. See [uuid-rooted-metadata.md](uuid-rooted-metadata.md) for the
design rationale and [metadata-storage-structure.md](metadata-storage-structure.md)
for the on-disk layout.

### Path resolution

`filePath` values stored in metadata are absolute paths under the `/files`
volume (e.g. `/files/watch/movies/Movie.2024.mkv`). The `/files` prefix is
itself a per-host detail — moving the volume only requires re-mounting it
at the same path inside the container.

---

## API Design

### Endpoint catalogue

The router is in `internal/api/server.go`. Endpoint set, by surface:

```
Bootstrap / discovery
  GET  /health
  GET  /status
  GET  /leader
  GET  /urls
  GET  /services         (alias: /api/services)
  GET  /services/{name}
  GET  /services/cleanup/stats

Metadata (primary surface for other services)
  GET    /meta
  GET    /meta/{hash}
  PUT    /meta/{hash}
  PATCH  /meta/{hash}
  DELETE /meta/{hash}
  GET    /meta/{hash}/{key...}
  PUT    /meta/{hash}/{key...}
  DELETE /meta/{hash}/{key...}
  POST   /meta/{hash}/_add/{key...}

CID-addressed (public, auth-bypassed at the perimeter)
  GET    /api/meta/{cid}
  GET    /api/file/{cid}/info
  GET    /file/{cid}
  POST   /file/cid
  HEAD   /data/{hash}
  GET    /data/{hash}/path

Editor / KV / schema / snapshot
  GET /api/metadata/hash-ids
  GET /api/metadata/list
  POST /api/metadata/search
  POST /api/metadata/batch
  POST /api/metadata/clear
  {GET,PUT,DELETE} /api/metadata/{hashId}
  {GET,PUT} /api/metadata/{hashId}/property
  GET /api/kv/{info,keys,tree,search,find,value}
  PUT /api/kv/value
  DELETE /api/kv/value
  GET /api/kv/key/{key...}
  GET /api/schema
  POST /api/schema/rescan
  GET /api/snapshot/export
  POST /api/snapshot/import
  POST /api/snapshot/wipe

SSE event streams (HTTP-mirror of Redis Streams)
  GET /api/events/files
  GET /api/events/meta

Mounts (rclone-only)
  GET    /api/mounts
  POST   /api/mounts
  GET    /api/mounts/{id}
  PUT    /api/mounts/{id}        (also PATCH)
  DELETE /api/mounts/{id}
  POST   /api/mounts/{id}/{mount,unmount,safe-unmount,scan}
  GET    /api/mounts/rclone/remotes

Watchers
  {GET,POST} /api/watchers
  {GET,PUT,DELETE} /api/watchers/{id}
  POST /api/watchers/{id}/{scan,reset}
  POST /api/watchers/{scan-all,reset-all}
  (deprecated: /api/scan/trigger, /api/scan/status — see watcher/handlers.go:32)

Admin
  POST /api/admin/migrate-dual-roots

WebDAV
  /webdav/...   (GET, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND)
```

No `/metrics` endpoint is exposed. Observability is via container logs +
the `/health` and `/status` JSON.

### Auth perimeter

In the dev and prod stacks Caddy + an nginx-hash-lock sidecar enforce
OIDC auth in front of meta-core. The following paths bypass auth (so
peers and unauthenticated tooling can reach them):

- `/health`
- `/webdav/*`
- `/meta/*`
- `/file/cid`
- `/api/file/{cid}` and `/api/meta/{cid}`
- (`/api/events/files`, `/api/events/meta` are also auth-bypassed but
  **should not** be exposed publicly via Caddy; they're inside-only.)

See `docs/api-mediated-access.md` "Auth" for the rationale.

### SSE event streams — wire contract

`/api/events/files` and `/api/events/meta` are SSE mirrors of the
`file:events` and `meta:events` Redis Streams. The contract
(`internal/api/sse_events.go`):

- One SSE event per Redis Stream entry.
- `id:` is the opaque `<ms>-<seq>` Redis Stream entry ID — clients echo
  it back via `Last-Event-ID` on reconnect.
- `event:` is the `type` field of the underlying entry.
- `data:` is the JSON of the rest of the entry.
- Heartbeats: SSE comment `:keep-alive\n\n` every 30s of silence.
- On reconnect with `Last-Event-ID`, the handler resumes from the next
  entry. If retention has trimmed past the cursor, the handler emits one
  `event: gap` payload before resuming from the oldest available entry.

External services use SSE with `Last-Event-ID` as the resume primitive.
A Redis consumer-group (XREADGROUP / XACK) exists *internally* — the
`MetaPublisher` uses one to drain keyspace notifications — but it is not
the external contract.

### WebDAV URLs

`/urls` exposes two WebDAV URLs (`internal/leader/election.go`):

| Field | Purpose | Construction |
|---|---|---|
| `webdavUrl` | External access | `{BASE_URL}/webdav` (or `http://{ip}:{API_PORT}/webdav` if `BASE_URL` is unset) |
| `webdavUrlInternal` | Container-to-container | `http://{hostname}:{META_CORE_HTTP_PORT}/webdav` |

Container plugins should use `webdavUrlInternal`. Browsers / external
clients use `webdavUrl` (which goes through nginx + Caddy).

WebDAV caching is handled by nginx's `proxy_cache` (see
`docker/nginx.conf`), not by a Go cache layer. There is no
`internal/cache` package.

---

## Implementation

### Why Go

| Requirement | Go advantage |
|---|---|
| Language-agnostic | HTTP API, no runtime dependency on callers |
| Single binary | Static compilation |
| Low resource | ~10-20 MB resident |
| HTTP server | stdlib `net/http` + gorilla/mux |
| Redis client | go-redis |
| WebDAV | `golang.org/x/net/webdav` |

### Repo layout

```
packages/meta-core/
├── cmd/meta-core/main.go         entry point
├── internal/
│   ├── api/                      HTTP server + handlers + SSE
│   ├── cid/                      CID parsing + algorithm ranking
│   ├── config/                   env-driven configuration
│   ├── discovery/                service registry + dead-service cleaner
│   ├── events/                   meta:events stream publisher
│   ├── leader/                   LeaderInfoProvider (no election logic)
│   ├── mounts/                   rclone mount manager + handlers + stats
│   ├── schema/                   live per-field schema indexer
│   ├── snapshot/                 snapshot export / import / wipe
│   ├── storage/                  Redis wrapper + UUID + CID reverse index + schema gate
│   ├── watcher/                  scanner + dispatcher + midhash + state
│   ├── watchers/                 polling-based watcher configs
│   └── webdav/                   /webdav/* handler
├── docker/
│   ├── entrypoint.sh
│   ├── leader-election.sh        the actual leader election
│   ├── mount-watcher.sh
│   ├── nginx.conf
│   └── supervisord.conf
├── editor/                       React metadata browser (Vite)
├── dashboard/                    React status dashboard (Vite)
├── docs/
└── go.mod
```

### Startup sequence (`cmd/meta-core/main.go`)

```
1. Load configuration from environment
2. Build LeaderInfoProvider          (no election; bash gate already won)
3. Connect to local Redis            (retry up to 30× / 1s)
4. EnsureSchemaVersion               (abort if Redis layout is stale)
5. Start service discovery + dead-service cleaner
6. NewServer + Server.Start          (boots watcher, mounts, WebDAV, API)
7. On first connection: start MetaPublisher + schema Indexer,
   republish existing metadata onto meta:events
8. Wait for SIGINT/SIGTERM
9. Shutdown in reverse order
```

### Process management

Redis is not spawned by the Go binary; supervisord starts it before
meta-core (`docker/supervisord.conf`). The Go binary connects to
`localhost:6379` and retries until Redis is reachable.

---

## Failure Modes

### Container restart

Supervisord restarts crashed processes (meta-core, redis, nginx, rclone)
individually. The bash flock is held by supervisord's process tree, so
internal crashes don't trigger re-election.

### Redis crash

Supervisord restarts redis. The Go binary's connect retry handles the
window. Persistence is RDB + AOF on the `/meta-core` volume, so no data is
lost across restarts.

### Container crash

Kernel releases the flock; a standby container (if any) can take over.
The dev compose runs a single instance, so the recovery path is "restart
the container."

### Schema mismatch

`EnsureSchemaVersion` refuses to boot. Operators wipe with
`pnpm run clean:all` (or equivalent) and restart. This is an alpha-stage
hard-fail; there is no automated migration path.

### Shared volume unavailable

All meta-core operations fail. Other services see meta-core as unhealthy
via `/health`; metadata I/O and SSE event streams stop.

---

## Configuration Reference

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `META_CORE_PATH` | `/meta-core` | Shared volume root. |
| `FILES_PATH` | `/files` | Files volume root. |
| `SERVICE_NAME` | `meta-core` | Identity in the service registry. |
| `SERVICE_VERSION` | `1.0.0` | Reported in `/status`. |
| `API_PORT` | `8180` | External port baked into `baseUrl`. |
| `BASE_URL` | _empty_ | Overrides constructed `baseUrl` (HTTPS perimeter). |
| `REDIS_PORT` | `6379` | Local Redis port. |
| `META_CORE_HTTP_PORT` | `9000` | Go HTTP+SSE+WebDAV port. |
| `META_CORE_HTTP_HOST` | `127.0.0.1` | HTTP bind address. |
| `HEALTH_CHECK_INTERVAL_MS` | `5000` | Internal health-loop cadence. |
| `HEARTBEAT_INTERVAL_MS` | `30000` | Service registry heartbeat. |
| `STALE_THRESHOLD_MS` | `60000` | Age past which a registry entry is stale. |
| `CLEANUP_INTERVAL_MS` | `600000` | Dead-service cleaner cadence. |
| `DEAD_SERVICE_THRESHOLD_MS` | `600000` | Age past which a stale entry is deleted. |
| `WATCH_INTERVAL_MS` | `1000` | Watcher poll cadence. |
| `DEBOUNCE_MS` | `30000` | File-event debounce window. |
| `ENABLE_FILE_WATCHER` | `true` | Disable to suppress the in-process watcher. |
| `ELECTION_RETRY_SECS` | `5` | Bash flock retry interval (consumed by `docker/leader-election.sh`, not the Go binary). |

### File paths under `/meta-core`

| Path | Purpose |
|---|---|
| `locks/kv-leader.lock` | Flock target. |
| `locks/kv-leader.info` | Leader API URL (plaintext). |
| `db/redis/{dump.rdb, appendonly.aof}` | Redis persistence. |
| `services/*.json` | Service registry. |
| `mounts/mounts.json` | rclone mount configuration. |
| `mounts/errors/{id}.error` | Mount error logs. |
| `watchers.json` | Polling watcher configuration. |
| `cache/` | nginx `proxy_cache` directory. |

---

## Summary

meta-core consolidates leader gating, metadata storage, event mirroring,
and file access for MetaMesh into one container with a typed HTTP+SSE
surface. The bash flock gate guarantees single-leader semantics without
an external consensus system. UUID-rooted storage with CID aliasing
treats all content hashes symmetrically and makes any-CID resolution
O(1). API-mediated access ensures no other service ever needs to speak
Redis.
