# meta-core

Standalone Go service that owns Redis, the HTTP+SSE metadata surface, the
User Data Layer store and signing keystore, and a WebDAV file server for the
MetaMesh dev/prod stack.

## Overview

meta-core runs as its own container (`metacore-app`). Other MetaMesh services
(meta-sort, meta-fuse, meta-stremio, meta-dup, meta-search, meta-share,
meta-gateway, meta-watch, …) locate it over UDP multicast (beacon v2 —
its `metamesh.core` advertise carries the full `/urls` payload) and then call its
HTTP API. `META_CORE_URL` on a client pins it instead. Redis is not published
outside the container — all metadata I/O is mediated by the HTTP/SSE API
(see [`docs/api-mediated-access.md`](docs/api-mediated-access.md)).

### Key capabilities

| Feature | Description |
|---------|-------------|
| **Redis-volume mutex** | Bash `flock` loop in `docker/leader-election.sh` only `exec`s supervisord on the winner, so two containers sharing one `/meta-core` volume can never run two Redis writers. It is a mutex, not discovery — nothing reads the lock. |
| **Redis owner** | Supervisord starts Redis with AOF + RDB persistence inside the container; meta-core connects to it on localhost. |
| **HTTP API** | Typed REST surface (gorilla/mux) for metadata, KV browsing, snapshots, schema inference, mounts, watchers, neighbours, identity, UDL, and file access by CID. |
| **In-memory search index** | `POST /api/metadata/search` matches against an in-memory snapshot of every record, kept current by incremental refreshes (full reconcile every 30 min) — no Redis round-trip per record on the request path (`internal/api/search_index.go`). |
| **SSE event streams** | `/api/events/files` and `/api/events/meta` mirror the underlying Redis Streams so external services never speak Redis. |
| **WebDAV server** | `/webdav/*` exposes the `/files` volume (read+write) for cross-service file access. nginx in front of meta-core handles caching. |
| **Service discovery** | beacon v2: UDP multicast advertise/listen on `239.255.99.1:9099`, neighbours served at `/neighbors`. The file-based registry under `/meta-core/services/*.json` it replaced is gone. |
| **File watcher / scanner** | Recursive scan + MidHash256 computation; emits events on `file:events`. Watched roots are POSTed watcher configs (`/meta-core/watchers.json`), not env vars. |
| **Mount management** | rclone-only mount manager (SMB rendered into `:smb:`, plus pre-defined remotes). Read-only by construction. |
| **UUID-rooted storage** | Roots are UUIDv7 (Crockford Base32, ULID layout); CIDs are reverse-index aliases. Schema-version sentinel refuses to boot against stale data. |
| **Identity keystore** | Multi-account secp256k1 signing keys under `/meta-core/identity/accounts/`, created from the dashboard's Identity menu (never auto-generated). A private key is returned only by generate, or by reveal against a proof-of-possession signature (delete needs the same proof); `/api/identity/sign` refuses challenge-domain payloads. |
| **User Data Layer** | Version-gated store for per-user signed (and, for private-tier keys, encrypted) records — My List, Continue Watching, ratings — indexed by user and CID. meta-core never inspects the crypto. See [User Data Layer](../../docs/project-architecture/user-data-layer.md). |
| **Dashboard + editor** | nginx serves a React dashboard at `/` and the standalone metadata editor at `/editor/`. |

### Design characteristics

- **Single writer** — flock on the `/meta-core` volume; no external consensus.
- **HTTP-only externally** — Redis is never published by the compose stacks;
  debug with `docker exec metacore-app redis-cli …`.
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
│  leader-election.sh  (flock /meta-core/locks/kv-leader.lock,             │
│                       then exec supervisord)                             │
│  supervisord                                                             │
│   ├─ redis-server        (AOF + RDB on /meta-core/db/redis)              │
│   ├─ nginx  :80          (dashboard, /editor, API proxy,                 │
│   │                       proxy_cache for /webdav, rclone GUI /rclone)   │
│   ├─ rclone rcd          (mount daemon, RC API on :5572)                 │
│   ├─ mount-watcher.sh    (rclone mount lifecycle)                        │
│   └─ meta-core (Go)      ─►  HTTP API + SSE + WebDAV on :9000            │
│                          ─►  UDP advertise/listen 239.255.99.1:9099      │
└──────────────────────────────────────────────────────────────────────────┘
                                       │
                ┌──────────────────────┴──────────────────────┐
                │  /meta-core (meta-core's own volume)        │
                │   locks/kv-leader.lock                      │
                │   db/redis/                                 │
                │   identity/accounts/*.json                  │
                │   mounts/{mounts.json, errors/}             │
                │   watchers.json                             │
                │   cache/ (nginx proxy_cache)                │
                └─────────────────────────────────────────────┘

   meta-sort / meta-fuse / meta-stremio / meta-search / meta-share / …
      │  1. hear the metamesh.core advertise on UDP multicast (carries /urls)
      └─ 2. HTTP + SSE (+ WebDAV) to metacore-app — never Redis, never the volume
```

## Quick start

### Build

```bash
make build           # build static binary to bin/meta-core
make docker          # build container image as meta-core:1.0.0 + :latest + :local
make test            # run Go unit tests
```

`make docker` tags the image as `:1.0.0`, `:latest`, **and** `:local`. The
`:local` tag is the one the sub-stack docker-compose files reference
(meta-share federation, meta-read and meta-listen dev stacks); the meta-gateway
dev stack (`docker-compose.gateway.yml`) uses `:latest`. Without these tags the
sub-stacks can't see a freshly-built image by name.

> **Source-change → recreate.** `make docker` rebuilds and re-tags the
> image, but **already-running containers stay on the old image SHA** —
> `docker compose up -d` won't recreate them unless the compose file
> itself changed. After a meta-core source change, force-recreate the
> downstream containers so they pick up the new binary:
>
> ```bash
> make docker
> docker compose -f packages/meta-gateway/dev/docker-compose.gateway.yml \
>   up -d --force-recreate metagateway-hub-metacore
> docker compose -f packages/meta-share/dev/docker-compose.yml \
>   --profile share-10node up -d --force-recreate metashare-core metashare-core-test
> docker compose -f packages/meta-read/dev/docker-compose.yml \
>   up -d --force-recreate metaread-dev-core
> docker compose -f packages/meta-listen/dev/docker-compose.yml \
>   up -d --force-recreate metalisten-dev-core
> ```
>
> The symptom of a missed recreate is missing API fields on `/urls`
> (e.g. `webdavUrlInternal` absent → meta-gateway's
> `store_poster_for_record` silently fails and torznab posters never
> land). Audit recipe:
>
> ```bash
> docker inspect meta-core:local --format '{{.Id}}'                # fresh SHA
> docker ps --filter ancestor=meta-core:local --format '{{.Names}}'   # repeat for :latest
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

Environment variables (from `internal/config/config.go` unless noted):

| Variable | Default | Description |
|---|---|---|
| `META_CORE_PATH` | `/meta-core` | meta-core's own volume root (lock, Redis data, identity, mounts, watchers, cache). No other service mounts it. |
| `FILES_PATH` | `/files` | Files volume root. |
| `SERVICE_NAME` | `meta-core` | Name in the beacon v2 advertise. |
| `SERVICE_VERSION` | `1.0.0` | Reported via `/status`. |
| `API_PORT` | `8180` | External port baked into the constructed `baseUrl` when `BASE_URL` is unset. |
| `BASE_URL` | _empty_ | Overrides the constructed `baseUrl` (the Caddy/HTTPS perimeter URL). |
| `META_CORE_PUBLIC_URL` | _empty_ | Browser-facing nav URL in the announce; wins over `BASE_URL` (e.g. a debug-direct port with no Caddy). |
| `REDIS_PORT` | `6379` | Local Redis port. |
| `META_CORE_HTTP_PORT` | `9000` | Go HTTP+SSE+WebDAV port. |
| `META_CORE_HTTP_HOST` | `0.0.0.0` | HTTP bind. |
| `ENABLE_FILE_WATCHER` | `true` | Disable to suppress the in-process watcher entirely (`POST /api/files/register` stays available). |
| `ENABLE_UDP_DISCOVERY` | `true` | beacon v2 advertise + listen. |
| `BEACON_GROUP` / `BEACON_PORT` | `239.255.99.1` / `9099` | Multicast group and UDP port. |
| `BEACON_INTERVAL_MS` | `10000` | Unsolicited advertise interval. |
| `META_CORE_INDEX_EXCLUDE_PREFIXES` | `categories/newznab/` | Comma-separated field prefixes left out of the in-memory search index; set empty to index everything (`internal/storage/client.go`). |
| `ELECTION_RETRY_SECS` | `5` | flock retry interval (read by `docker/leader-election.sh`, not the Go binary). |

`HEALTH_CHECK_INTERVAL_MS`, `HEARTBEAT_INTERVAL_MS`, `STALE_THRESHOLD_MS`,
`CLEANUP_INTERVAL_MS`, `DEAD_SERVICE_THRESHOLD_MS`, `WATCH_INTERVAL_MS` and
`DEBOUNCE_MS` are still parsed by `internal/config/config.go` but nothing reads
them (leftovers of the file-based registry); setting them has no effect. There
is no `META_CORE_REDIS_PORT` / `META_CORE_SERVICE_NAME` /
`META_CORE_LEADER_TIMEOUT`, and no `WATCH_PATHS` — watched folders are watcher
configs created via `/api/watchers`.

## API reference

All endpoints below are registered in `internal/api/server.go` (mounts,
watcher and watchers routes in their own packages' `handlers.go`). Examples assume
the in-container Go port `:9000`; from the host use
`https://metacore-dev.localhost:8083` (Caddy) or `http://localhost:18083`
(debug-direct).

### Health, leader, URLs

```bash
GET  /health           # storage + role
GET  /status           # version, uptime, etc.
GET  /leader           # current leader info (hostname, PID, timestamps)
GET  /urls             # baseUrl, apiUrl, webdavUrl, webdavUrlInternal (redisUrl is intentionally empty)
GET  /neighbors        # neighbours heard over UDP (beacon v2)
GET  /api/neighbors    # same, under /api
GET  /api/stats        # dashboard counters (see below)
```

`/services`, `/api/services`, `/services/{name}` and `/services/cleanup/stats`
are **gone** — the registration-and-heartbeat registry they served was replaced
by beacon v2 (`/neighbors`). nginx answers an unknown non-`/api` path
with the dashboard's `index.html`, so a stale caller gets `200 text/html` rather
than a 404; that is what made the dashboard fail with
`SyntaxError: Unexpected token '<'` until 1.0.21.

#### `GET /api/stats`

Every headline counter in one request, split by cost:

```jsonc
{
  "records": 88709,        // SCARD file:__index__ — metadata roots, file or not
  "redisKeys": 3115447,    // DBSIZE — the flat per-field keys dominate
  "redisMemory": "588.74M",
  "identities": 3,         // signing accounts on disk
  "files": { "count": 4864, "totalSize": 64587914424 },  // null until first sweep
  "udlUsers": 5,           // accounts holding User Data Layer records
  "sweptAt": 1789402401970,
  "sweeping": false
}
```

The first four are O(1) commands and are live on every call. `files` and
`udlUsers` need a keyspace walk (~0.5s and ~3s respectively on an 88k-record
box), so they are served from a single-flight cache refreshed in the background
with a 60s TTL — the walk is **never** on the request path, because the
dashboard polls and ten open tabs must not cost ten Redis walks. Prefer this
over `/api/kv/info`, which does one `GET` per root (6s+) and sums `sizeByte`
across records that share a file.

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
POST   /api/files/register        # mint + alias one file under /files/plugin by midhash
                                  # (the watcher's path for one file; works with the watcher OFF)
HEAD   /data/{hash}                   # existence + size
GET    /data/{hash}/path              # absolute path on /files
```

(There is no `/data/{hash}/stream`; bulk reads go through `/webdav/*` or
the `/file/{cid}` reverse-lookup path.)

### Editor / KV browser surface

```bash
GET    /api/metadata/hash-ids
GET    /api/files/tuples
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
GET /api/events/poll      # DEPRECATED long-poll over file:events — use SSE
```

### Identity (signing keystore)

Reached through meta-search's identity proxy (and directly by meta-gateway,
meta-read, meta-listen) for record signing, UDL writes and keypair sign-in.

```bash
GET    /api/identity               # status (never the private key)
GET    /api/identity/accounts
POST   /api/identity/generate      # the only call that returns a new private key
POST   /api/identity/import
POST   /api/identity/challenge     # one-shot challenge for proof-of-possession
POST   /api/identity/reveal        # needs proof-of-possession
DELETE /api/identity               # needs proof-of-possession; reports purged UDL counts
POST   /api/identity/sign
POST   /api/identity/sign-batch
GET    /api/identity/aead-key
```

### User Data Layer

Records arrive signed (private-tier ones encrypted) as opaque base64 CBOR;
writes are version-gated (stale → `409 stale_version`, or `accepted:false` per
cell in a batch). Redis model in `internal/storage/udl.go`.

```bash
GET  /api/udl/record?uid&cid&key
PUT  /api/udl/record
PUT  /api/udl/records                      # batch
GET  /api/udl/user/{uid}/key/{key}
GET  /api/udl/user/{uid}/cid/{cid}
GET  /api/udl/user/{uid}/all
GET  /api/udl/user/{uid}/count
GET  /api/udl/cid/{cid}/users
GET  /api/udl/cid/{cid}/aggregate          # meta-watch Aggregate wire shape
GET  /api/udl/users/stats
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
POST   /api/admin/migrate-domain-screen  # domain film|tv → screen, workForm backfill (idempotent)
POST   /api/admin/migrate-user-cids   # backfill udl:idx:user:<uid>:cids; until run, pre-index
                                      # profiles export as EMPTY. Returns {fixed}
POST   /api/admin/sweep-literature-echoes[?apply=true]
       # strip query echoes off literature cards: anilistid on site cards,
       # workcid/chapterNumber/volumeNumber/chapterStart/chapterEnd on any card.
       # Dry-run unless apply=true; returns {dryRun, planned, fixed, byKey}
GET/PUT/DELETE /webdav/...            # mounted on /files
```

No `/metrics` endpoint is exposed; observability is via container logs and
the `/health` / `/status` JSON.

## Redis-volume mutex

`docker/leader-election.sh` is a bash flock loop, not a Go-level state
machine. When `flock` succeeds on `/meta-core/locks/kv-leader.lock`, the
script `exec`s supervisord; a second container mounting the same volume
retries every `ELECTION_RETRY_SECS` and stays dormant. When the process tree
dies, the kernel releases the lock. There is no follower path inside the Go
binary.

The lock is a **mutex, not service discovery** — it only stops two Redis
writers landing on one RDB/AOF. Nothing reads it, and the `kv-leader.info`
file it used to publish is gone: siblings locate meta-core over UDP
(see [beacon-v2.md](../../docs/project-architecture/beacon-v2.md)).
`internal/leader` keeps its name for history, but only builds the `/urls`
payload (`baseUrl`, `apiUrl`, `webdavUrl`, `webdavUrlInternal`) that `/urls`
and the UDP announce share.

## Metadata storage shape (one-line summary)

Roots are opaque UUIDv7 strings; every property is a separate Redis
STRING at `file:<uuid>/<property>`; every known CID is a key-set member
`file:<uuid>/cids/<bareCid>` plus a reverse-index entry `cid:<bareCid> → <uuid>`
(the canonical CID is derived by rank on read, never stored). Full details in
[`docs/metadata-storage-structure.md`](docs/metadata-storage-structure.md)
and design rationale in [`docs/uuid-rooted-metadata.md`](docs/uuid-rooted-metadata.md).
The authoritative registry of field semantics and value formats lives in
the repo-root [`METADATA_KEYS.md`](../../METADATA_KEYS.md).

## Internal packages

| Package | Purpose |
|---------|---------|
| `cmd/meta-core` | Entry point; Redis connect + schema sentinel + UDP discovery + service wiring. |
| `internal/config` | Env-driven configuration, path helpers. |
| `internal/leader` | `LeaderInfoProvider` — builds the `/urls` payload from hostname/IP/config; no election logic. |
| `internal/meshdisco` | beacon v2 node (advertise, probe, neighbour map) — the Go port of the spec. |
| `internal/storage` | Redis wrapper (`client.go`), UUIDv7 minting, CID reverse index (`cid_resolution.go`), schema sentinel, field indexes + search-index bulk reads, UDL store (`udl.go`, `udl_admin.go`), stats, one-shot migrations/sweeps. |
| `internal/cid` | CID parsing + rank ladder for canonical-CID selection (`cid-rank-vectors.json`, guarded by the meta-root `scripts/check-cid-vectors.sh`). |
| `internal/identity` | Multi-account secp256k1 keystore, challenge/proof-of-possession, signature verification. |
| `internal/events` | Keyspace-notification → `meta:events` stream publisher (`meta_publisher.go`). |
| `internal/api` | HTTP server + all handlers + SSE event endpoints + in-memory search index + `/api/stats`. |
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
5. Start beacon v2 node (unless ENABLE_UDP_DISCOVERY=false)
6. Construct API server (identity keystore migration, watcher, watchers,
   mounts, WebDAV)
7. Start API server + pollers + search-index refresh loop; start
   MetaPublisher + schema Indexer, then warm field indexes and republish
   existing metadata onto meta:events in the background
8. Wait for SIGINT/SIGTERM, shutdown in reverse order
```

## Dependencies

| Dependency | Purpose |
|------------|---------|
| `github.com/gorilla/mux` | HTTP router |
| `github.com/redis/go-redis/v9` | Redis client |
| `github.com/google/uuid` | UUIDv7 minting |
| `github.com/decred/dcrd/dcrec/secp256k1/v4`, `golang.org/x/crypto`, `github.com/mr-tron/base58` | Identity keys, signing, AEAD key derivation |
| `golang.org/x/net` | WebDAV support |
| `github.com/alicebob/miniredis/v2` | In-memory Redis for tests |

Runtime requirements: Go 1.21+ at build time; the container ships Redis,
rclone, nginx, and supervisord alongside the binary, plus the React dashboard
and metadata editor (built from `dashboard/` and `editor/` in the Dockerfile).

## License

Part of the MetaMesh project.
