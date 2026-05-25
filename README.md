# meta-core

Standalone Go service that owns Redis, the leader-info file, the HTTP+SSE
metadata surface, and a WebDAV file server for the MetaMesh dev/prod stack.

## Overview

meta-core runs as its own container (`metacore-app`). Other MetaMesh services
(meta-sort, meta-fuse, meta-stremio, meta-dup, meta-share) discover it by
reading `/meta-core/locks/kv-leader.info` and then call its HTTP API. Redis
is not exposed outside the container — all metadata I/O is mediated by the
HTTP API.

### Key capabilities

| Feature | Description |
|---------|-------------|
| **Leader gate** | Bash `flock` loop in `docker/leader-election.sh` only `exec`s supervisord on the winner. The Go binary therefore only ever runs as leader. |
| **Redis owner** | Supervisord starts Redis with AOF + RDB persistence inside the container; meta-core connects to it on localhost. |
| **HTTP API** | Typed REST surface (gorilla/mux) for metadata, KV browsing, snapshots, schema inference, mounts, watchers, services, and file access by CID. |
| **SSE event streams** | `/api/events/files` and `/api/events/meta` mirror the underlying Redis Streams so external services never speak Redis. |
| **WebDAV server** | `/webdav/*` exposes the `/files` volume (read+write) for cross-service file access. nginx in front of meta-core handles caching. |
| **Service discovery** | File-based registry under `/meta-core/services/*.json` with heartbeat + dead-service cleanup. |
| **File watcher / scanner** | Recursive scan + MidHash256 computation; emits events on `file:events`. |
| **Mount management** | rclone-only mount manager (SMB rendered into `:smb:`, plus pre-defined remotes). Read-only by construction. |
| **UUID-rooted storage** | Roots are UUIDv7 (Crockford Base32, ULID layout); CIDs are reverse-index aliases. Schema-version sentinel refuses to boot against stale data. |

### Design characteristics

- **Single-leader** — flock on a shared volume; no external consensus.
- **HTTP-only externally** — Redis is bound to the container, never published.
- **Schema-gated boot** — `EnsureSchemaVersion` aborts startup when the
  on-disk Redis layout predates the current build (alpha clean-wipe policy;
  see `docs/uuid-rooted-metadata.md`).
- **Static binary** — CGO disabled, single Go binary plus a thin Alpine
  image with `redis-server`, `rclone`, nginx, and supervisord.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────────┐
│                    metacore-app  (standalone container)                  │
│                                                                          │
│  supervisord                                                             │
│   ├─ leader-election.sh  (flock /meta-core/locks/kv-leader.lock,         │
│   │                       writes kv-leader.info, exec supervisord)      │
│   ├─ redis-server        (AOF + RDB on /meta-core/db/redis)              │
│   ├─ nginx               (TLS termination, proxy_cache for /webdav)      │
│   ├─ rclone rcd          (mount daemon, RC API)                          │
│   ├─ mount-watcher.sh    (rclone mount lifecycle)                        │
│   └─ meta-core (Go)      ─►  HTTP API + SSE + WebDAV on :9000            │
└──────────────────────────────────────────────────────────────────────────┘
                                       │
                ┌──────────────────────┴──────────────────────┐
                │  /meta-core (shared volume)                 │
                │   locks/kv-leader.{lock,info}               │
                │   db/redis/                                 │
                │   services/*.json                           │
                │   mounts/{mounts.json, errors/}             │
                │   watchers.json                             │
                │   cache/ (nginx proxy_cache)                │
                └─────────────────────────────────────────────┘
                                       ▲
        ┌──────────────────────────────┼────────────────────────────────┐
        │                              │                                │
┌─────────────┐               ┌─────────────┐                ┌─────────────┐
│  meta-sort  │               │  meta-fuse  │                │ meta-stremio│
│             │               │             │                │             │
│  reads      │  HTTP+SSE     │  reads      │  HTTP+SSE      │  reads      │
│  leader.info├──────────────►│  leader.info├───────────────►│  leader.info│
└─────────────┘               └─────────────┘                └─────────────┘
```

## Quick start

### Build

```bash
make build           # build static binary to bin/meta-core
make docker          # build container image as meta-core:1.0.0 + :latest + :local
make test            # run Go unit tests
```

`make docker` tags the image as `:1.0.0`, `:latest`, **and** `:local`. The
`:local` tag is the one every sub-stack docker-compose.yml references
(meta-share 10-peer federation, meta-gateway dev stacks); without it the
sub-stacks can't see a freshly-built image by name.

> **Source-change → recreate.** `make docker` rebuilds and re-tags the
> image, but **already-running containers stay on the old image SHA** —
> `docker compose up -d` won't recreate them unless the compose file
> itself changed. After a meta-core source change, force-recreate the
> downstream containers so they pick up the new binary:
>
> ```bash
> make docker
> docker compose -f packages/meta-gateway/dev/docker-compose.yml \
>   up -d --force-recreate metagateway-generic-core
> docker compose -f packages/meta-gateway/dev/docker-compose.torznab.yml \
>   up -d --force-recreate metagateway-torznab-core
> docker compose -f packages/meta-share/dev/docker-compose.yml \
>   --profile share-10node up -d --force-recreate metashare-core metashare-core-test
> ```
>
> The symptom of a missed recreate is missing API fields on `/urls`
> (e.g. `webdavUrlInternal` absent → meta-gateway's
> `store_poster_for_record` silently fails and torznab posters never
> land). Audit recipe:
>
> ```bash
> docker inspect meta-core:local --format '{{.Id}}'                # fresh SHA
> docker ps --filter ancestor=meta-core:local --format '{{.Names}}'
> # any container whose Image SHA doesn't match the fresh SHA needs --force-recreate.
> ```

### Dev container

In the MetaMesh dev stack, the container is brought up by `dev/docker-compose.yml`:

```bash
cd dev && ./scripts/start.sh
```

The user-facing URL is `https://metacore-dev.localhost:8083`; the direct-backend
HTTP port (no auth, bypasses Caddy) is `http://localhost:18083`.

## Configuration

Environment variables consumed by `internal/config/config.go`:

| Variable | Default | Description |
|---|---|---|
| `META_CORE_PATH` | `/meta-core` | Shared volume root (locks, db, services, mounts, cache). |
| `FILES_PATH` | `/files` | Files volume root. |
| `SERVICE_NAME` | `meta-core` | Identity in the service registry. |
| `SERVICE_VERSION` | `1.0.0` | Reported via `/status` and registry. |
| `API_PORT` | `8180` | External port baked into the leader-info `baseUrl`. |
| `BASE_URL` | _empty_ | Overrides the constructed `baseUrl` if set (used for HTTPS perimeter). |
| `REDIS_PORT` | `6379` | Local Redis port. |
| `META_CORE_HTTP_PORT` | `9000` | Go HTTP+SSE+WebDAV port. |
| `META_CORE_HTTP_HOST` | `127.0.0.1` | HTTP bind. |
| `HEALTH_CHECK_INTERVAL_MS` | `5000` | Internal health-loop cadence. |
| `HEARTBEAT_INTERVAL_MS` | `30000` | Service registry heartbeat. |
| `STALE_THRESHOLD_MS` | `60000` | Age past which a registry entry is marked stale. |
| `CLEANUP_INTERVAL_MS` | `600000` | Dead-service cleaner cadence. |
| `DEAD_SERVICE_THRESHOLD_MS` | `600000` | Age past which a stale entry is deleted. |
| `WATCH_INTERVAL_MS` | `1000` | Watcher poll cadence. |
| `DEBOUNCE_MS` | `30000` | File-event debounce window. |
| `ENABLE_FILE_WATCHER` | `true` | Disable to suppress the in-process watcher entirely. |

Note: there is no `META_CORE_REDIS_PORT`, no `META_CORE_SERVICE_NAME`, and
no `META_CORE_LEADER_TIMEOUT`. The Redis URL is discovered locally; service
identity uses `SERVICE_NAME`; leader timeout is governed by the bash flock
retry interval (`ELECTION_RETRY_SECS`, default `5`).

## API reference

All endpoints below are served by `internal/api/server.go`. Examples assume
the in-container Go port `:9000`; from the host use
`https://metacore-dev.localhost:8083` (Caddy) or `http://localhost:18083`
(debug-direct).

### Health, leader, URLs

```bash
GET  /health           # storage + role
GET  /status           # version, uptime, etc.
GET  /leader           # current leader info (hostname, PID, timestamps)
GET  /urls             # baseUrl, apiUrl, webdavUrl, webdavUrlInternal (redisUrl is intentionally empty)
GET  /services         # registered services + heartbeats
GET  /api/services     # alias for /services
GET  /services/{name}  # one service
GET  /services/cleanup/stats
```

### Metadata — primary surface

These are the routes other services use. `{hash}` may be a UUID root or
any registered CID alias; CID aliases resolve to the underlying UUID
before reads/writes (`internal/storage/cid_resolution.go`).

```bash
GET    /meta                          # list of root IDs
GET    /meta/{hash}                   # full document (flat keys → nested JSON)
PUT    /meta/{hash}                   # replace document
PATCH  /meta/{hash}                   # merge into document
DELETE /meta/{hash}                   # delete root + all CID aliases
GET    /meta/{hash}/{key...}          # single property (key may contain "/")
PUT    /meta/{hash}/{key...}          # set single property
DELETE /meta/{hash}/{key...}          # delete single property
POST   /meta/{hash}/_add/{key...}     # add value to set-valued property
```

CID-addressed access (public; auth-bypassed in the Caddy perimeter):

```bash
GET    /api/meta/{cid}                # resolve CID → metadata document
GET    /api/file/{cid}/info           # CID resolution metadata
GET    /file/{cid}                    # serve file bytes (range-aware)
POST   /file/cid                      # compute CID for an uploaded file
HEAD   /data/{hash}                   # existence + size
GET    /data/{hash}/path              # absolute path on /files
```

(There is no `/data/{hash}/stream`; bulk reads go through `/webdav/*` or
the `/file/{cid}` reverse-lookup path.)

### Editor / KV browser surface

```bash
GET    /api/metadata/hash-ids
GET    /api/metadata/list
POST   /api/metadata/search
POST   /api/metadata/batch
POST   /api/metadata/clear
GET    /api/metadata/{hashId}
PUT    /api/metadata/{hashId}
DELETE /api/metadata/{hashId}
GET    /api/metadata/{hashId}/property
PUT    /api/metadata/{hashId}/property

GET    /api/kv/info
GET    /api/kv/keys
GET    /api/kv/tree
GET    /api/kv/search
GET    /api/kv/find
GET    /api/kv/value
PUT    /api/kv/value
DELETE /api/kv/value
GET    /api/kv/key/{key...}

GET    /api/schema
POST   /api/schema/rescan

GET    /api/snapshot/export
POST   /api/snapshot/import
POST   /api/snapshot/wipe
```

### SSE event streams

The HTTP-mirror of the Redis Streams. External services never read Redis
directly — they subscribe here with `Last-Event-ID` for resume. See
[`docs/api-mediated-access.md`](docs/api-mediated-access.md) for the wire
contract (one event per stream entry, opaque IDs, heartbeats, gap
signalling).

```bash
GET /api/events/files     # file:events stream (watcher events)
GET /api/events/meta      # meta:events stream (metadata mutations)
```

### Mounts

```bash
GET    /api/mounts
POST   /api/mounts
GET    /api/mounts/{id}
PUT    /api/mounts/{id}       # also PATCH
DELETE /api/mounts/{id}
POST   /api/mounts/{id}/mount
POST   /api/mounts/{id}/unmount
POST   /api/mounts/{id}/safe-unmount
POST   /api/mounts/{id}/scan
GET    /api/mounts/rclone/remotes
```

### Watchers

```bash
GET    /api/watchers
POST   /api/watchers
GET    /api/watchers/{id}
PUT    /api/watchers/{id}
DELETE /api/watchers/{id}
POST   /api/watchers/{id}/scan
POST   /api/watchers/{id}/reset
POST   /api/watchers/scan-all
POST   /api/watchers/reset-all
```

The legacy `/api/scan/trigger` and `/api/scan/status` still respond but
return a deprecation pointer at the `/api/watchers/*` routes
(`internal/watcher/handlers.go:32-33`).

### Admin / WebDAV

```bash
POST   /api/admin/migrate-dual-roots  # reunify stranded midhash-rooted entries
GET/PUT/DELETE /webdav/...            # mounted on /files
```

No `/metrics` endpoint is exposed; observability is via container logs and
the `/health` / `/status` JSON.

## Leader gate

Leader election is a bash flock loop in `docker/leader-election.sh`, not a
Go-level state machine. When `flock` succeeds on `kv-leader.lock`, the
script writes the leader API URL to `kv-leader.info` and `exec`s
supervisord. When the process tree dies, the kernel releases the lock and
any standby instance can take over. There is no follower path inside the
Go binary — there is no `OnBecomeLeader` / `OnBecomeFollower` callback API.

`kv-leader.info` is a single plaintext line containing the API base URL:

```
http://10.0.1.50:9000
```

Other services read this file, then call `GET /urls` for full discovery.

The dev compose runs a single `metacore-app`; the flock primitive is
retained as a correctness anchor (one process owns Redis and the
writable state under `/meta-core`), not as live failover.

## Metadata storage shape (one-line summary)

Roots are opaque UUIDv7 strings; every property is a separate Redis
STRING at `file:<uuid>/<property>`; every known CID for a file is a
reverse-index entry `cid:<algorithm>:<value> → <uuid>`. Full details in
[`docs/metadata-storage-structure.md`](docs/metadata-storage-structure.md)
and design rationale in [`docs/uuid-rooted-metadata.md`](docs/uuid-rooted-metadata.md).
The authoritative registry of field semantics and value formats lives in
the repo-root [`METADATA_KEYS.md`](../../METADATA_KEYS.md).

## Internal packages

| Package | Purpose |
|---------|---------|
| `cmd/meta-core` | Entry point; Redis connect + schema sentinel + service wiring. |
| `internal/config` | Env-driven configuration, path helpers. |
| `internal/leader` | `LeaderInfoProvider` (builds the leader info from local hostname/IP/config; no election logic). |
| `internal/storage` | Redis wrapper (`client.go`), UUIDv7 minting (`uuid.go`), CID reverse index (`cid_resolution.go`), schema sentinel (`schema_version.go`), dual-root migration (`dual_root_migration.go`). |
| `internal/cid` | CID parsing + algorithm ranking for canonical-CID selection. |
| `internal/discovery` | Service registry + dead-service cleaner. |
| `internal/events` | Keyspace-notification → `meta:events` stream publisher (`meta_publisher.go`). |
| `internal/api` | HTTP server + all handlers + SSE event endpoints. |
| `internal/schema` | Live per-field schema indexer (consumes `meta:events`). |
| `internal/snapshot` | Snapshot export / import / wipe. |
| `internal/watcher` | File system scanner, MidHash256 computation, dispatcher, state registry. |
| `internal/watchers` | Polling-based watcher configurations (manager + poller + handlers). |
| `internal/mounts` | rclone mount manager, lifecycle handlers, stats poller. |
| `internal/webdav` | WebDAV handler (mounted at `/webdav/*`; nginx handles caching upstream). |

### Startup sequence

```
1. Load configuration from environment
2. Build LeaderInfoProvider (no election — the bash gate already won)
3. Connect to local Redis (retry up to 30× / 1s)
4. EnsureSchemaVersion — abort if the existing Redis layout is stale
5. Start service discovery + dead-service cleaner
6. Construct API server (initialises watcher, mounts, WebDAV)
7. Start API server; on first connect, start MetaPublisher + schema Indexer
   and republish existing metadata onto meta:events
8. Wait for SIGINT/SIGTERM, shutdown in reverse order
```

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `github.com/gorilla/mux` | HTTP router |
| `github.com/redis/go-redis/v9` | Redis client |
| `github.com/google/uuid` | UUIDv7 minting |
| `golang.org/x/net` | WebDAV support |
| Go stdlib | context, net/http, os, sync, syscall |

Runtime requirements: Go 1.21+ at build time; the container ships Redis,
rclone, nginx, and supervisord alongside the binary.

## License

Part of the MetaMesh project.
