# Redis Metadata Storage Structure

How meta-core lays out file metadata in Redis. This document covers the
on-disk shape (UUID-rooted flat string keys, CID reverse index,
per-root sets) and the schema-version gate. It does **not** enumerate
field semantics — that's the job of the repo-root
[`METADATA_KEYS.md`](../../../METADATA_KEYS.md), the authoritative key
registry for everyone who writes to or reads from metadata.

For the design rationale (why UUIDs instead of content hashes, why a
reverse index), see [`uuid-rooted-metadata.md`](uuid-rooted-metadata.md).
For the external-access contract (HTTP-only, SSE for events), see
[`api-mediated-access.md`](api-mediated-access.md).

## Quick summary

- **Roots are UUIDv7** encoded as 26-char Crockford Base32 (ULID layout),
  not content hashes. Examples in this doc use the placeholder
  `01JKR8XW5T4QMV7AHNJ2DEFGHK`.
- **Properties are flat Redis STRING keys**, one per leaf:
  `file:<uuid>/<property/path>` → string value. *Not* Redis Hashes.
- **CIDs are reverse-index aliases**: `cid:<bareCid>` → `<uuid>`
  (a Redis STRING per known CID; no algorithm prefix — the multicodec
  already carries it).
- **Per-root sets**: `file:<uuid>/cids/<bareCid>` (a *key-set*: one STRING
  `"true"` per known CID, midhash included), `file:<uuid>/duplicates`
  (alternate paths with the same content), plus `file:__index__` (every UUID).
  There is **no stored `canonical_cid`** — it is derived by rank on read.
- **A schema-version sentinel** (`meta-core:schema-version`) refuses to
  boot against legacy data.

Implementation lives in `internal/storage/client.go`,
`internal/storage/cid_resolution.go`, `internal/storage/uuid.go`, and
`internal/storage/schema_version.go`.

## Key structure

### Roots

```
file:<uuid>/<property/path>   →   Redis STRING (always)
```

UUIDs are produced by `storage.NewUUID()` (`internal/storage/uuid.go`):
UUIDv7 → 26-char Crockford Base32 (skips I/L/O/U; ULID-compatible). The
time-sortable prefix lets you `KEYS file:01JKR8*` to find roots minted
in a given window, which restored some of the debug-ability lost when
midhash256 stopped being the root.

Properties use `/` as the path separator. Nested objects in source code
are flattened to flat keys on write and reconstructed on read. Examples:

```
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/filePath                          = "/files/watch/movies/Inception.2010.mkv"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/sizeByte                          = "15032385536"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/mtimeNano                         = "1729872000000000000"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/title                             = "Inception"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/fileinfo/duration                 = "8878.5"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/fileinfo/streamdetails/video/0/codec = "h264"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/tmdb/poster                       = "bafkreih..."
```

### Reverse-index aliases (CIDs)

Every CID known for a file is registered as its own Redis STRING, keyed by
the **bare CID** — no algorithm prefix. A CIDv1 already encodes its hash
function in the multicodec, so naming it again in the key would be
redundant (and would let the same digest be indexed under two spellings):

```
cid:<bareCid>   →   <uuid>
```

Examples:

```
cid:bafkreih5kapbjzq...   →   01JKR8XW5T4QMV7AHNJ2DEFGHK
cid:bagacbabaec7v3...     →   01JKR8XW5T4QMV7AHNJ2DEFGHK
```

The reverse index makes `GET /api/meta/<cid>` an O(1) lookup for any
registered CID, independent of which hash algorithm — there is no primary.
The auto-alias hook (`maybeAddAliasFromFieldLocked` in
`internal/storage/cid_resolution.go`, called from `SetProperty`,
`SetMetadataFlat` and `MergeMetadataFlat`) registers the alias for any
field whose name starts with `cids/`, so writers emit key-set members
without knowing the reverse index exists.

> ⚠ The hook matches **only** the `cids/` prefix. A legacy `cid_*` or
> `midhash256` *named* field written to a root is stored verbatim and is
> **never indexed** — the record becomes unresolvable by that CID. Writers
> must collapse such fields to `cids/<cid>` members before the write. See
> `METADATA_KEYS.md` §14.13.

### Per-root sets

```
file:<uuid>/cids/<bareCid>    →   STRING "true" — one key per known CID (a key-set, not a Redis SET)
file:<uuid>/duplicates        →   Redis SET of alternate paths with same content
```

`cids` is a **key-set**: one flat `file:<uuid>/cids/<cid>` STRING per member,
which keeps it conflict-free to merge across peers (a SET would not be).
The record's own address (the midhash) is just the `0x1000` member — it has
no separate field.

There is **no stored `canonical_cid`**. The canonical CID is *derived on
read*: `GetMetadataDocument` (`internal/storage/cid_resolution.go`) ranks the
key-set's members through `internal/cid/rank.go` (`cid.Better`) and returns
the winner as the `canonical_cid` field of the JSON response only. Nothing
reconciles or persists it — an earlier `reconcileCanonicalCIDLocked` did, and
was removed with the flat `cid_*` keys.

### Global index

```
file:__index__   →   Redis SET of all UUIDs
```

`internal/storage/client.go::GetAllHashIDs` reads this set to enumerate
every file without scanning the keyspace.

```bash
SMEMBERS file:__index__         # all UUIDs
SCARD    file:__index__         # total file count
SISMEMBER file:__index__ <uuid>
```

### Schema-version sentinel

```
meta-core:schema-version   →   STRING (currently "2")
```

Set at first boot, checked on every subsequent boot
(`internal/storage/schema_version.go`). If the sentinel is missing but
`file:__index__` is non-empty, the gate refuses to boot (legacy data).
If the sentinel is present but doesn't match `SchemaVersion`, the gate
also refuses to boot. Alpha-stage policy: operators wipe and restart;
there is no automated migration.

## Why flat STRING keys?

The store does not use Redis Hashes. The reasons are:

1. **Field-level keyspace notifications.** Redis publishes
   `__keyspace@0__:<key>` on every SET/DEL of a STRING key. With Hashes
   you'd only get one notification on the hash itself. meta-core's
   `MetaPublisher` (`internal/events/meta_publisher.go`) consumes these
   notifications and publishes structured events to the `meta:events`
   Redis Stream.
2. **Selective updates without read-modify-write.** A plugin writing
   `tmdb/poster` doesn't have to read the rest of the document.
3. **Compatibility with the CID alias hooks.** Field writes can trigger
   alias-maintenance work in `SetProperty` without the plugin caring.

## Reads and writes

The Go API in `internal/storage/client.go`:

| Method | Behaviour |
|---|---|
| `GetMetadataFlat(uuid)` | `SCAN` for `file:<uuid>/*`, `MGET` the values, return `map[string]string`. |
| `SetMetadataFlat(uuid, m)` | Pipeline `SET` per field; `SADD file:__index__`; auto-register an alias for every `cids/<cid>` member. |
| `MergeMetadataFlat(uuid, m)` | Like `SetMetadataFlat` but doesn't pre-clear; used by `PATCH /meta/{hash}`. |
| `GetProperty(uuid, field)` | `GET file:<uuid>/<field>`. |
| `SetProperty(uuid, field, value)` | `SET file:<uuid>/<field>`; auto-register alias if the field is a CID. |
| `DeleteRoot(uuid)` | `SCAN` + `DEL` for `file:<uuid>/*`, unmap every reverse-index entry. |
| `Mint(filePath, size, mtimeNano)` | `NewUUID()` + write `filePath/sizeByte/mtimeNano` + add to index. |
| `AddAlias(uuid, "<bareCid>")` | Set `cid:<bareCid>` → `<uuid>` and the key-set member `file:<uuid>/cids/<bareCid>` = `"true"`. Nothing to reconcile — canonical is derived on read. |
| `ResolveRoot("<bareCid>")` | `GET cid:<bareCid>` → uuid (returns the input unchanged if it is already a UUID root). |
| `GetMetadataDocument(uuid)` | Flat fields + `cids` hoisted to an array + `canonical_cid` **derived** by `cid.Better` rank + `duplicates`. Backs `GET /api/meta/{cid}`. |

## Reconstruction (flat → nested)

When the HTTP API returns a document, it reconstructs the flattened
storage back into a nested JSON object:

```
flat                                          nested
─────────────────────────────────────         ───────────────────────────
title                       = "Inception"     title: "Inception"
fileinfo/duration           = "8878.5"        fileinfo: { duration: "8878.5",
fileinfo/streamdetails/                                  streamdetails: { video: { 0: {
  video/0/codec             = "h264"                       codec: "h264", ... } }, ... } }
streams/0/language          = "eng"           streams: [ { language: "eng" }, ... ]
```

Rules:

- `/` is the path separator.
- Numeric path segments (`0`, `1`, ...) become array indices on read.
- All values are strings on the wire; type interpretation is the
  caller's job (and is documented per-field in `METADATA_KEYS.md`).
- `null`/`undefined` round-trip as empty strings.

## Plugin contribution flow

Plugins don't talk to Redis. They write metadata via meta-core's HTTP
API (`PUT /meta/{hash}/{key...}` or `PATCH /meta/{hash}`). Each plugin
writes under a prefix:

```
{plugin-id}/<field>                 single value
{plugin-id}/<group>/<field>         grouped
{plugin-id}/<group>/<n>/<field>     indexed (array)
```

See [`METADATA_KEYS.md`](../../../METADATA_KEYS.md) for the full,
authoritative list of fields each plugin writes (content identification,
file stats, video classification, anime detection, subtitle references,
Jellyfin metadata, FFmpeg stream details, etc.) and the value formats
(`int-string`, `lang3`, `path-relfiles`, `csv-set`, ...). This doc
deliberately does not duplicate that registry — it would drift.

## Event streams

Two Redis Streams are produced inside meta-core:

| Stream | Producer | Contents |
|---|---|---|
| `file:events` | `internal/watcher` dispatcher | `add` / `change` / `delete` / `rename` / `reset` events for files on the `/files` volume. |
| `meta:events` | `internal/events.MetaPublisher` | Field-level metadata mutations derived from Redis keyspace notifications. |

External consumers do **not** read these streams directly. They subscribe
via SSE:

- `GET /api/events/files` — mirror of `file:events`
- `GET /api/events/meta` — mirror of `meta:events`

The SSE wire contract uses `Last-Event-ID` for resume (the opaque
`<ms>-<seq>` Redis Stream entry ID). Heartbeats are SSE comments
(`:keep-alive\n\n`) every 30s of silence. If retention has trimmed past
a client's cursor, the handler emits one `event: gap` payload before
resuming. See `internal/api/sse_events.go` and
[`api-mediated-access.md`](api-mediated-access.md) for the full
contract.

Internally, meta-core uses a Redis consumer group (XREADGROUP / XACK)
inside `MetaPublisher` to drain keyspace notifications, but this is not
the external surface — external services use SSE.

## Debugging

```bash
# Roots
docker exec metacore-app redis-cli SMEMBERS 'file:__index__'
docker exec metacore-app redis-cli SCARD    'file:__index__'
docker exec metacore-app redis-cli SRANDMEMBER 'file:__index__'

# Full document for one root (flat scan)
docker exec metacore-app redis-cli --scan --pattern 'file:01JKR8XW5T4QMV7AHNJ2DEFGHK/*'

# One property
docker exec metacore-app redis-cli GET 'file:01JKR8XW5T4QMV7AHNJ2DEFGHK/fileinfo/duration'

# Confirm keys are STRING (not HASH)
docker exec metacore-app redis-cli TYPE 'file:01JKR8XW5T4QMV7AHNJ2DEFGHK/filePath'
# string

# Reverse-index lookup (bare CID — no algorithm prefix)
docker exec metacore-app redis-cli GET 'cid:bafkreih...'      # → uuid

# The cids key-set (one STRING per member, NOT a Redis SET — SMEMBERS won't work)
docker exec metacore-app redis-cli --scan --pattern 'file:01JKR8XW5T4QMV7AHNJ2DEFGHK/cids/*'

# canonical_cid is NOT stored. Derive it via the API instead:
curl -k https://metacore-dev.localhost:8083/api/meta/<cid> | jq '.canonical_cid, .cids'

# Schema-version sentinel
docker exec metacore-app redis-cli GET 'meta-core:schema-version'

# Streams (server-side; external consumers should use SSE)
docker exec metacore-app redis-cli XRANGE 'file:events' - + COUNT 10
docker exec metacore-app redis-cli XRANGE 'meta:events' - + COUNT 10

# HTTP equivalents — preferred
curl -k 'https://metacore-dev.localhost:8083/api/meta/<cid>'      # CID → document
curl -k 'https://metacore-dev.localhost:8083/meta'                # list root IDs
curl -k 'https://metacore-dev.localhost:8083/meta/<uuid>'         # document by UUID
curl -k 'https://metacore-dev.localhost:8083/api/events/meta'     # live SSE
curl -k 'https://metacore-dev.localhost:8083/api/kv/info'         # KV stats
```

## Key implementation files

| Component | Path |
|---|---|
| Redis client + flat-key ops | `internal/storage/client.go` |
| UUIDv7 minting | `internal/storage/uuid.go` |
| CID reverse index + aliases + canonical-CID reconciliation | `internal/storage/cid_resolution.go` |
| Schema-version gate | `internal/storage/schema_version.go` |
| Dual-root migration (legacy clean-up) | `internal/storage/dual_root_migration.go` |
| Keyspace-notification → `meta:events` publisher | `internal/events/meta_publisher.go` |
| Watcher → `file:events` dispatcher | `internal/watcher/dispatcher.go` |
| SSE event mirror | `internal/api/sse_events.go` |
| Field semantics (authoritative registry) | [`/METADATA_KEYS.md`](../../../METADATA_KEYS.md) |
