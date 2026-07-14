# UUID-Rooted Metadata with CID Aliasing

## Status

**Status:** Implemented (2026 alpha clean-wipe gate landed). Kept for design rationale.

> ⚠ **Two parts of this proposal were superseded before shipping.** This doc
> is the design record; it is *not* the schema reference. For what is actually
> stored, see `METADATA_KEYS.md` §2/§14.13 and
> [metadata-storage-structure.md](./metadata-storage-structure.md).
>
> 1. **`canonical_cid` is not a stored key.** The proposed reconciler
>    (`reconcileCanonicalCIDLocked`) was never kept — a stored scalar isn't
>    conflict-free to merge across peers. The canonical CID is **derived on
>    read** by ranking the `cids` members (`internal/cid/rank.go` →
>    `cid.Better`), and appears only in the `GET /api/meta/{cid}` response.
> 2. **`cids` is a key-set, not a Redis SET, and holds bare CIDs, not
>    `<algo>:<value>` tokens.** One flat STRING per member:
>    `file:<uuid>/cids/<bareCid>` = `"true"`. The reverse index is likewise
>    `cid:<bareCid>` → uuid. A CIDv1's multicodec already names its hash
>    function, so the `<algo>:` prefix was redundant.
>
> Text below that references either shape describes the *proposal*, not the
> code. The rank ordering and everything else did ship as written.

## Motivation

Today, every metadata entry in Redis is keyed by a content hash, specifically
midhash256:

```
file:midhash256:abc.../filePath
file:midhash256:abc.../title
file:midhash256:abc.../fileinfo/duration
file:midhash256:abc.../tmdb/poster
...
```

Other digests (sha256, IPFS CIDs, BitTorrent infohashes) live as fields
*inside* that entry:

```
file:midhash256:abc.../cid_sha256 = sha256:def...
file:midhash256:abc.../cid_ipfs   = bafy...
```

Two problems with this:

1. **midhash256 is privileged by accident.** It IS the key, while every other
   CID is a secondary field. There's nothing intrinsically special about
   midhash256 — it's just the first hash computed. Hash-algorithm changes, or
   new digest types added by plugins, force rewrites of every root. The
   "promotion" workarounds people reach for (re-keying entries when a stronger
   hash arrives) solve the wrong problem; the privilege itself is the issue.

2. **CID lookup is O(n).** Resolving "given any CID, return that file's
   metadata" currently scans every indexed entry (see
   `storage/client.go::LookupPathByCID`, which acknowledges this in a
   comment). Acceptable for admin tooling, unacceptable for a public API
   surface — especially when peers on meta-share may query by any CID variant.

The product requirement is clear: **any CID a client knows about must resolve
to that file's metadata in O(1).** IPFS-compatible CIDs are the externally
meaningful identity for content retrieval; the internal storage layout should
not bias toward any one digest type.

## Goal

A single externally-meaningful contract:

```
GET /api/meta/<cid>  →  full metadata document for that file
```

Where `<cid>` may be any supported type (midhash256, sha256, IPFS, btih,
sha3, future additions). All CID types are treated symmetrically. No CID type
is "the key."

## Design

### Two-tier key structure

The root key becomes an opaque internal UUID; CIDs are aliases that point to
it.

```
file:<uuid>/<property...>     ← canonical storage (UUIDv7, never disclosed externally)
cid:<cid> → <uuid>            ← reverse index (one Redis key per known CID)
```

The metadata keyspace under `file:<uuid>/` remains open and flat, exactly as
today — anything that wrote `file:<midhash>/fileinfo/duration` now writes
`file:<uuid>/fileinfo/duration`. Only the root identifier changes.

**Example layout:**

```
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/filePath          = /watch/Inception.mkv
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/size              = 4500000000
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/mtime             = 1729872000000000000
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/cids/bagacbabaec…  = "true"    ← midhash (record address)
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/cids/bafybeigd…    = "true"    ← sha2-256
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/duplicates        = SET{/watch/dup1.mkv, /watch/dup2.mkv}
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/title             = "Inception"
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/fileinfo/duration = 7200.5
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/tmdb/poster       = bafy...
file:01JKR8XW5T4QMV7AHNJ2DEFGHK/subtitle/eng      = ...

cid:bagacbabaec…  → 01JKR8XW5T4QMV7AHNJ2DEFGHK
cid:bafybeigd…    → 01JKR8XW5T4QMV7AHNJ2DEFGHK
```

*(As shipped — the proposal originally wrote `cids` as a Redis SET of
`<algo>:<value>` tokens plus a `canonical_cid` scalar. See the Status note.)*

### UUID format: UUIDv7

UUIDv7 chosen over UUIDv4 or 64-bit random:

- **128 bits**: collision-free under distributed minting; no central allocator.
- **Time-sortable prefix**: `KEYS file:01JKR8*` retrieves entries minted in a
  given window. Restores some of the debuggability lost by going opaque.
- **Monotonic-ish**: adjacent UUIDs cluster on disk; modestly better SCAN
  locality.

Encoded as a 26-character Crockford Base32 string (ULID-compatible) for
compactness and case-insensitive matching.

### New root fields owned by meta-core

The metadata schema is open — any writer can put any key under
`file:<uuid>/`. This proposal does not change that. It adds three keys whose
sole writer is meta-core itself:

| Key | Type (as shipped) | Purpose |
|-----|------|---------|
| `cids/<bareCid>` | STRING `"true"` — a *key-set*, one key per member | All CIDs known for this file, midhash included. Mirror of the `cid:*` reverse index. Source of truth for delete-time GC. **Proposed as a Redis SET of `<algo>:<value>` tokens; shipped as a key-set of bare CIDs** — a key-set merges conflict-free across peers and the multicodec already names the algorithm. |
| `duplicates` | Redis SET | Additional file paths whose content matches this entry. One UUID = one set of metadata; multiple physical files may share that content. Consumed by meta-dup. |
| ~~`canonical_cid`~~ | *(not stored)* | **Dropped.** Was to hold the externally-presented CID. Now derived on read by ranking the `cids` members; surfaced only on `GET /api/meta/{cid}`. |

Existing meta-core-owned fields (`filePath`, `size`, `mtime`, `midhash256`)
keep their names and writers. Plugin-written fields (`tmdb/*`, `fileinfo/*`,
`subtitle/<lang>`, …) are unaffected.

~~Redis SET is a departure from the all-strings convention in the current
storage doc, but `file:__index__` is already a SET — so the precedent exists
for using sets where they're the right structure.~~ **Reversed for `cids`.**
The all-strings convention won: `cids` ships as flat `cids/<cid>` STRING keys
so that two peers merging the same record can never disagree about set
membership. `duplicates` remained a Redis SET (it is leader-local and never
merged across peers).

### Canonical CID selection

The canonical CID is a label, not a key — so nothing external breaks when it
changes. **As shipped it is not written at all**: it is computed on every read
from the `cids` members, which makes the "update" cost zero and removes the
reconciler entirely.

Selection rule, highest-wins (this part shipped as designed —
`internal/cid/rank.go`):

1. IPFS CID (any variant)
2. SHA-256 / SHA-3 family
3. BitTorrent infohash (v2 preferred over v1)
4. midhash256

Ranked by **utility for external retrieval**, not by cryptographic strength.
midhash256 and sha256 are both 256-bit, but only sha256/IPFS CIDs are
externally meaningful for content addressing. btih ranks above midhash because
it enables swarm interop even if it's weaker as a digest.

~~Reconciler runs on the leader, recomputes `canonical_cid` whenever
`AddAlias` registers a new CID for an entry.~~ **Not shipped** — there is no
reconciler. `GetMetadataDocument` folds the members through `cid.Better` on
each read.

## Internal operations

The meta-core storage interface adds five operations. None are exposed
externally — they're internal to meta-core. External callers see only the
CID-based API.

```go
// Mint creates a new root with no aliases. Called when the watcher
// computes a midhash for a previously-unseen file. Returns the new UUID.
Mint(filePath string, size int64, mtime int64) (uuid string, err error)

// AddAlias registers a CID as resolving to this UUID. Updates both the
// cid:<cid> reverse index and the file:<uuid>/cids/<cid> key-set member.
// (As shipped: no reconciliation step — canonical is derived on read.)
AddAlias(uuid string, cid string) error

// GetByCID resolves any known CID to its UUID. Returns ("", nil) if unknown.
GetByCID(cid string) (uuid string, err error)

// MergeIfAliasExists is called when the watcher mints a fresh UUID for a
// file path but, on hashing, discovers the resulting CID already belongs
// to a different UUID. Merges the new (mostly-empty) UUID into the
// existing one, transferring filePath into the duplicates set, and
// returns the canonical UUID.
MergeIfAliasExists(newUUID, cid string) (canonicalUUID string, merged bool, err error)

// DeleteRoot removes the entire entry, including every cid:* alias and
// every file:<uuid>/* key. Used for file deletion events.
DeleteRoot(uuid string) error
```

### Lifecycle: file arrival

```
1. Watcher sees /watch/foo.mkv created.
2. Watcher computes midhash256 → "midhash256:abc...".
3. Watcher calls GetByCID("midhash256:abc...").
   → uuid_X (existing entry) or "" (new file).

4a. If existing (uuid_X):
    - Add "/watch/foo.mkv" to file:<uuid_X>/duplicates.
    - No new root created.
    - meta-dup is notified via the existing keyspace event channel.

4b. If new:
    - Mint("/watch/foo.mkv", size, mtime) → uuid_Y.
    - AddAlias(uuid_Y, "midhash256:abc...").
    - Plugins now process the file, referenced by midhash256:abc...
      (UUID is never disclosed to them.)

5. Later, the full-hash plugin computes sha256.
   - Plugin reports back: "for CID midhash256:abc..., sha256 is sha256:def...".
   - meta-core resolves midhash256:abc → uuid_Y via GetByCID.
   - meta-core calls AddAlias(uuid_Y, "sha256:def...").
   - Nothing else to do: the next read of this entry derives a canonical CID
     of sha2-256, because it outranks the midhash.
```

### Lifecycle: file deletion

```
1. Watcher sees /watch/foo.mkv deleted.
2. Resolve which entry owns this path:
   a. If /watch/foo.mkv is in some entry's duplicates set:
      - Remove from set. Done.
   b. If /watch/foo.mkv is the filePath of some entry:
      - If duplicates is non-empty: promote one duplicate to filePath.
      - If duplicates is empty: DeleteRoot(uuid).
        - Iterates the cids set, deletes each cid:<cid> reverse-index key.
        - Deletes every file:<uuid>/* key.
        - Removes uuid from file:__index__.
```

## Public API

```
GET /api/meta/<cid>
  → 200 { "filePath": "...", "canonical_cid": "...",
          "cids": [...], "title": "...", "tmdb": {...}, ... }
  → 404 if no such CID

GET /api/file/<cid>          (already exists — implementation now O(1))
  → streams file content
```

The UUID is **not** included in the response. Clients address everything by
CID; meta-core's choice of internal identifier remains a private
implementation detail and may change.

Auth: `/api/meta/<cid>` follows the same pattern as `/api/file/<cid>` and
bypasses the auth perimeter. It's the metadata twin of the content endpoint,
and meta-share calls it from federated search responses.

## Cross-service impact

This is a meta-core schema change. Other services interact only through CIDs,
so the impact is contained but real.

- **meta-sort**: writers must resolve plugin callbacks to a UUID before
  writing. The existing `LeaderClient` gains a `getByCID` cache.
- **meta-fuse**: VFS rebuild already iterates `file:__index__`; only the
  ID format changes.
- **meta-stremio**: queries by CID; needs no logic change beyond its storage
  client.
- **meta-dup**: gains a cleaner data source — duplicates are now explicit in
  the `duplicates` set, not implicit in colliding writes.
- **meta-search / meta-share**: announce the *derived* canonical CID rather
  than the midhash — each mirrors the Go rank in `ingest::canonical_cid_from_metadata`
  rather than reading a stored field. Federated search responses include all
  CIDs from the `cids` key-set so any peer querying by any digest gets a hit.
  (Written before the 2026-06 discovery/transport split; discovery is now
  meta-search's job.)
- **Plugins**: no change. They speak CIDs only.

## Migration

> **Historical — this sweep was never run.** The alpha shipped behind a
> clean-wipe gate (Redis was flushed and the library re-ingested), so no
> in-place migration code exists. Kept for the record.

One-time sweep, idempotent, runs on first boot after upgrade:

```
For each hashID in file:__index__ (current shape: midhash256:abc, sha256:def, …):
  1. Mint a fresh uuid_Y.
  2. AddAlias(uuid_Y, hashID).  The existing primary becomes an alias.
  3. For each cid_* field on the old entry, AddAlias(uuid_Y, that value).
  4. RENAME file:<hashID>/<property> → file:<uuid_Y>/<property> for every
     property (Lua script, one entry at a time; non-atomic across entries,
     but each entry stays internally consistent).
  5. Reconcile canonical_cid.
  6. Remove hashID from file:__index__, add uuid_Y.
```

Crash-recoverable: re-running the sweep finds entries with both old and new
roots and resumes by detecting which step completed (presence of
`file:<uuid>/cids` indicates step 3 finished; absence of any
`file:<hashID>/*` keys indicates step 4 finished).

## Decisions recorded

| Question | Decision | Notes |
|---|---|---|
| When is the UUID minted? | At first-hash | Pragmatic for the initial migration. Long-term target: at discovery, fully decoupled from hashing. Tracked as an open question below. |
| UUID format | UUIDv7, 26-char Crockford Base32 | Time-sortable, 128-bit, distributed-safe. |
| UUID visible externally? | No | Pure meta-core internal. Clients address by CID only. |
| Merge semantics | One UUID per content; additional paths into `duplicates` | One file (logical) = one metadata set. Multiple physical paths supported via `duplicates`. |
| Canonical CID rank | IPFS > sha-family > btih > midhash | Utility-weighted, not strength-weighted. |
| ~~`cids` and `duplicates` field type~~ | ~~Redis SET~~ → **`cids` is a key-set of bare CIDs; `duplicates` stayed a SET** | Reversed before shipping. Flat `cids/<cid>` STRING keys merge conflict-free across peers; a SET does not. See §14.13 of `METADATA_KEYS.md`. |
| ~~Stored `canonical_cid`~~ | **Dropped — derived on read** | A stored scalar is not conflict-free to merge. `GetMetadataDocument` ranks the `cids` members per request. |
| Reverse-index format | Flat `cid:<cid>` STRING keys | Simple, scan-friendly, no hot-key contention. |

## Open questions

- **Discovery-time UUID minting.** The cleaner long-term model: watcher mints
  a UUID at discovery (before hashing), then `AddAlias` runs when the midhash
  arrives. Requires an explicit "reconcile if alias already exists" step
  because two paths with the same content would each get their own UUID until
  hashing reveals the collision. Worth doing eventually; not required for
  this iteration.

- ~~**meta-share announce strategy.**~~ **Settled.** The record carries *every*
  member of `cids` on the wire (`Record.cids`) and addresses itself by the
  single derived canonical (`Record.cid`), so a peer querying by any digest
  gets a hit without the DHT footprint of announcing each one separately.

- **IPFS gateway endpoint.** Should meta-core expose `/ipfs/<cid>` as a
  content-streaming endpoint for external IPFS clients? Adjacent to this
  design but not part of it.

- **Keyspace-notification consumers.** Today's parsers (`schema/indexer.go`)
  handle `file:<type>:<hash>/...` with a special split rule for the colon
  inside `midhash256:abc`. With UUIDs (no colons), `parseFieldPath` simplifies
  — but any external consumer subscribed by hashID pattern needs updating.
  Inventory those before flipping the migration.

## Non-goals

- **Not** a content-replication or IPFS-pinning system. Registering an IPFS
  CID alias does not pin the file anywhere — it only states "if somebody
  hashes this file with IPFS's chunker, this is the CID they'd get."
- **Not** a versioning scheme. Metadata is a snapshot of current state;
  historical versions are out of scope.
- **Not** a strong-consistency system. Reverse-index writes happen under the
  leader lock; reads from followers may briefly miss a freshly-added alias.
