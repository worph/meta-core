package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metazla/meta-core/internal/cid"
	"github.com/redis/go-redis/v9"
)

// This file implements the CID-resolution layer that lives on top of the
// flat metadata storage in client.go. The shape is described in detail in
// docs/uuid-rooted-metadata.md; the short version:
//
//   - Roots are opaque UUIDv7s — file:<uuid>/<property> for every property
//   - Every known CID for a file is registered as a reverse-index entry:
//       cid:<algorithm>:<value> → <uuid>     (Redis STRING)
//     and mirrored in a per-root set for fast enumeration:
//       file:<uuid>/cids                     (Redis SET)
//   - A canonical_cid field tracks the best (rank-wise) CID to advertise
//     externally; updated by reconcileCanonicalCIDLocked whenever the set
//     changes
//
// Auto-alias hooks in SetProperty/SetMetadataFlat/MergeMetadataFlat catch
// any write of a cid_* or midhash256 field and register the alias, so
// plugins that just write their result fields don't need to know about
// the reverse index at all. The hook helpers are *Locked — they assume
// c.mu is already held by the caller, avoiding nested RLock (which can
// deadlock against a waiting writer).

// CIDIndexPrefix is the Redis key prefix for the reverse index. Combined
// with a CID token, it forms the full key: "cid:midhash256:bafk…".
const CIDIndexPrefix = "cid:"

// CIDsField is the per-root SET key that mirrors all CID aliases for a file.
// Stored at file:<uuid>/cids — read by reconcileCanonicalCIDLocked and by
// DeleteRoot to find every reverse-index entry that needs to be cleaned up.
const CIDsField = "cids"

// CanonicalCIDField is the per-root STRING key that stores the externally
// preferred CID for this file. Picked by cid.Better over the cids set.
const CanonicalCIDField = "canonical_cid"

// DuplicatesField is the per-root SET key holding additional file paths
// whose content hashes to the same CIDs as this root. The watcher writes
// here when it sees a new path for content that's already indexed; meta-dup
// reads from it.
const DuplicatesField = "duplicates"

// buildCIDIndexKey returns the absolute Redis key for a reverse-index entry.
// token is a CID in prefixed form ("midhash256:bafk…", "sha256:…", etc.).
func (c *Client) buildCIDIndexKey(token string) string {
	return c.buildKey(CIDIndexPrefix + token)
}

// Mint creates a new root with filePath/sizeByte/mtimeNano and registers it
// in the file index. Returns the new UUID. The caller is expected to follow
// up with AddAlias for at least one CID — a root without any alias is
// reachable only via the index, which works but defeats the point.
func (c *Client) Mint(filePath string, size, mtimeNano int64) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	uuid, err := NewUUID()
	if err != nil {
		return "", fmt.Errorf("uuid: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prefix := c.buildKeyPrefix(uuid)
	pipe := c.client.Pipeline()
	pipe.Set(ctx, prefix+"filePath", filePath, 0)
	pipe.Set(ctx, prefix+"sizeByte", strconv.FormatInt(size, 10), 0)
	pipe.Set(ctx, prefix+"mtimeNano", strconv.FormatInt(mtimeNano, 10), 0)
	pipe.SAdd(ctx, c.buildIndexKey(), uuid)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("mint pipeline: %w", err)
	}
	return uuid, nil
}

// AddAlias registers a CID token as resolving to uuid. Idempotent: writing
// the same alias twice is a no-op. After the alias is registered, the
// canonical_cid field is reconciled — picks the highest-ranked token in
// the cids set and writes it to canonical_cid if it changed.
func (c *Client) AddAlias(uuid, token string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.addAliasLocked(ctx, uuid, token)
}

// addAliasLocked is the lock-free body of AddAlias. The caller must already
// hold c.mu (read or write). Splits out so the alias-maintenance hooks
// invoked from SetProperty / SetMetadataFlat / MergeMetadataFlat can reuse
// the work without re-entering the lock — nested RLock can deadlock
// against a waiting writer.
func (c *Client) addAliasLocked(ctx context.Context, uuid, token string) error {
	if uuid == "" || token == "" {
		return fmt.Errorf("uuid and token are required")
	}
	if cid.AlgorithmOf(token) == "" {
		return fmt.Errorf("token %q is not in <algorithm>:<value> form", token)
	}

	// Self-pointing alias guard. If the caller is trying to register
	// `cid:<algo>:<value>` → `<value>` (i.e. the root IS the cid value),
	// that's the dual-root failure mode in disguise — it means somebody
	// wrote /meta/<value> without first resolving via ResolveRoot. Refuse
	// the alias write; leave the existing alias (if any) alone. Still
	// add to this root's cids set so the field-level write succeeds.
	if uuid == cid.ValueOf(token) {
		if _, err := c.client.SAdd(ctx, c.buildKeyPrefix(uuid)+CIDsField, token).Result(); err != nil {
			return fmt.Errorf("sadd cids set: %w", err)
		}
		return c.reconcileCanonicalCIDLocked(ctx, uuid)
	}

	// Guard against the dual-root pattern: if `cid:<token>` already points
	// at a DIFFERENT uuid, don't silently overwrite. The historical bug:
	// meta-sort writing cid_midhash256=<v> to root "<v>" caused the hook
	// to register cid:midhash256:<v> → <v>, severing the watcher's prior
	// cid:midhash256:<v> → <real-uuid> alias. With write-path resolution
	// now in place callers shouldn't hit this anymore, but the guard is
	// cheap and prevents regression if a new caller bypasses ResolveRoot.
	indexKey := c.buildCIDIndexKey(token)
	existing, err := c.client.Get(ctx, indexKey).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read existing alias: %w", err)
	}
	if existing != "" && existing != uuid {
		// Existing alias points elsewhere. Leave it alone and just add the
		// token to this root's cids set so it's still discoverable via the
		// root → cids enumeration.
		if _, err := c.client.SAdd(ctx, c.buildKeyPrefix(uuid)+CIDsField, token).Result(); err != nil {
			return fmt.Errorf("sadd cids set: %w", err)
		}
		return c.reconcileCanonicalCIDLocked(ctx, uuid)
	}

	prefix := c.buildKeyPrefix(uuid)
	pipe := c.client.Pipeline()
	pipe.Set(ctx, indexKey, uuid, 0)
	pipe.SAdd(ctx, prefix+CIDsField, token)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("add alias pipeline: %w", err)
	}
	return c.reconcileCanonicalCIDLocked(ctx, uuid)
}

// ResolveRoot maps a hash supplied by a caller (a meta-sort write, an
// editor lookup, an SSE consumer) to the actual storage root key.
//
// Callers historically send a bare midhash256 value (e.g.
// "bagacba…") because that's what the watcher publishes on file:events.
// The UUID-rooted-metadata design (see docs/uuid-rooted-metadata.md)
// keeps roots opaque and aliases CIDs into the reverse index:
//
//	cid:midhash256:bagacba… → 01KSBHM*
//
// Without resolution, every /meta/<midhash> write creates a parallel
// midhash-rooted entry that the watcher's UUID never sees. The editor
// then shows two entries per file: the (rich) midhash root and the
// (empty) UUID root containing only `duplicates`.
//
// Behaviour:
//   - empty hash → empty (caller's problem)
//   - hash matches cid:midhash256:<hash> in the reverse index → return UUID
//   - otherwise return hash unchanged (legacy / explicit-UUID writes)
//
// Errors are converted to "no resolution" — if Redis is down or the
// lookup fails we let the caller proceed with the bare hash and surface
// the Redis failure on the actual operation.
func (c *Client) ResolveRoot(hash string) string {
	if hash == "" {
		return hash
	}
	uuid, err := c.GetByCID("midhash256:" + hash)
	if err != nil || uuid == "" {
		return hash
	}
	return uuid
}

// GetByCID resolves a CID token to the UUID of its root. Returns ("", nil)
// when the token is unknown; the empty string is the "not found" signal
// rather than a typed error so callers can branch without errors.As.
func (c *Client) GetByCID(token string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}
	if token == "" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uuid, err := c.client.Get(ctx, c.buildCIDIndexKey(token)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get cid index: %w", err)
	}
	return uuid, nil
}

// AddDuplicatePath records that path is another physical location whose
// content matches uuid. Returns true if path was newly added (not already
// in the set). The watcher calls this when it sees a file whose midhash
// matches an existing root but at a different filePath.
func (c *Client) AddDuplicatePath(uuid, path string) (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}
	if uuid == "" || path == "" {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	added, err := c.client.SAdd(ctx, c.buildKeyPrefix(uuid)+DuplicatesField, path).Result()
	if err != nil {
		return false, fmt.Errorf("sadd duplicates: %w", err)
	}
	return added > 0, nil
}

// DeleteRoot removes everything keyed off uuid: every file:<uuid>/* property,
// every reverse-index entry listed in the cids set, and the index membership.
// Idempotent — a uuid with no remaining keys is a successful no-op.
func (c *Client) DeleteRoot(uuid string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}
	if uuid == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKeyPrefix(uuid)

	// 1. Read the cids set first — once we delete file:<uuid>/* we lose the
	//    list of reverse-index entries to clean up. Tolerate a missing set
	//    (empty list = nothing to unmap).
	tokens, err := c.client.SMembers(ctx, prefix+CIDsField).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("smembers cids: %w", err)
	}

	// 2. Delete every reverse-index entry. Build the keys upfront, then DEL
	//    in one shot so we don't pay per-RTT.
	if len(tokens) > 0 {
		keys := make([]string, 0, len(tokens))
		for _, t := range tokens {
			keys = append(keys, c.buildCIDIndexKey(t))
		}
		if err := c.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("del cid index: %w", err)
		}
	}

	// 3. Scan and delete every file:<uuid>/* key.
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return fmt.Errorf("scan root keys: %w", err)
		}
		if len(keys) > 0 {
			if err := c.client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("del root keys: %w", err)
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	// 4. Remove from the file index set.
	if err := c.client.SRem(ctx, c.buildIndexKey(), uuid).Err(); err != nil {
		return fmt.Errorf("srem index: %w", err)
	}
	return nil
}

// reconcileCanonicalCIDLocked reads the cids set, picks the highest-ranked
// token, and updates the canonical_cid field if it changed. Called from
// addAliasLocked after a new alias is registered. Caller must hold c.mu.
func (c *Client) reconcileCanonicalCIDLocked(ctx context.Context, uuid string) error {
	prefix := c.buildKeyPrefix(uuid)

	tokens, err := c.client.SMembers(ctx, prefix+CIDsField).Result()
	if err != nil {
		return fmt.Errorf("smembers cids: %w", err)
	}
	if len(tokens) == 0 {
		return nil
	}

	best := tokens[0]
	for _, t := range tokens[1:] {
		best = cid.Better(best, t)
	}

	canonicalKey := prefix + CanonicalCIDField
	current, err := c.client.Get(ctx, canonicalKey).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get canonical_cid: %w", err)
	}
	if current == best {
		return nil
	}
	if err := c.client.Set(ctx, canonicalKey, best, 0).Err(); err != nil {
		return fmt.Errorf("set canonical_cid: %w", err)
	}
	return nil
}

// maybeAddAliasFromFieldLocked inspects a (field, value) pair written into
// a root. If it looks like a CID field — name is "midhash256" or starts
// with "cid_" — it registers the implied CID token as an alias. Plugins
// write CID fields without knowing about the reverse index; this hook
// keeps the index in sync transparently.
//
// Caller must hold c.mu. Errors are intentionally swallowed at the call
// site (logged, not returned): an out-of-sync reverse index is recoverable
// via a sweep, while a failed property write is not.
func (c *Client) maybeAddAliasFromFieldLocked(ctx context.Context, uuid, field, value string) error {
	token := cidTokenFromField(field, value)
	if token == "" {
		return nil
	}
	return c.addAliasLocked(ctx, uuid, token)
}

// maybeAddAliasesFromMetadataLocked bulk-applies maybeAddAliasFromFieldLocked
// over every entry in a metadata map. Used by SetMetadataFlat /
// MergeMetadataFlat after the underlying pipeline write succeeds. Caller
// must hold c.mu.
func (c *Client) maybeAddAliasesFromMetadataLocked(ctx context.Context, uuid string, metadata map[string]string) {
	for k, v := range metadata {
		_ = c.maybeAddAliasFromFieldLocked(ctx, uuid, k, v)
	}
}

// cidTokenFromField turns a (field-name, raw-value) pair into a normalized
// CID token "<algorithm>:<value>". Returns "" for non-CID fields.
//
// Acceptable inputs:
//   - field="midhash256", value="bafk…"           → "midhash256:bafk…"
//   - field="cid_sha256",  value="bafk…"          → "sha256:bafk…"
//   - field="cid_ipfs",    value="ipfs:bafy…"     → "ipfs:bafy…" (trusted)
//
// Plugins that already use the prefixed form get their value passed through;
// plugins that store the bare CID get prefixed from the field name. The
// detection heuristic is just "value contains a colon" — base32lower CIDs
// never contain colons, so this is unambiguous.
func cidTokenFromField(field, value string) string {
	if value == "" {
		return ""
	}
	var algo string
	switch {
	case field == "midhash256":
		algo = "midhash256"
	case strings.HasPrefix(field, "cid_"):
		algo = strings.TrimPrefix(field, "cid_")
	default:
		return ""
	}
	if algo == "" {
		return ""
	}
	if strings.IndexByte(value, ':') > 0 {
		return value
	}
	return cid.Token(algo, value)
}

// MetadataDocument is the complete state of one file as the public API
// returns it: every flat string field, plus the cids and duplicates sets
// hoisted into typed arrays. Distinct from GetMetadataFlat (which only
// returns string keys) — clients querying /api/meta/<cid> want the SETs
// expanded too, since "what other CIDs identify this file" and "what other
// paths point at this content" are core to the response.
type MetadataDocument struct {
	Flat       map[string]string `json:"metadata"`
	CIDs       []string          `json:"cids"`
	Duplicates []string          `json:"duplicates"`
}

// GetMetadataDocument returns the full document for a root: every string
// property under file:<uuid>/* plus the cids and duplicates SETs. Returns
// nil if the root has no keys at all (deleted or never existed).
func (c *Client) GetMetadataDocument(uuid string) (*MetadataDocument, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	if uuid == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	flat, err := c.getMetadataFlatInternal(ctx, uuid)
	if err != nil {
		return nil, err
	}
	prefix := c.buildKeyPrefix(uuid)

	cids, err := c.client.SMembers(ctx, prefix+CIDsField).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("smembers cids: %w", err)
	}
	dups, err := c.client.SMembers(ctx, prefix+DuplicatesField).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("smembers duplicates: %w", err)
	}

	if len(flat) == 0 && len(cids) == 0 && len(dups) == 0 {
		return nil, nil
	}
	return &MetadataDocument{
		Flat:       flat,
		CIDs:       cids,
		Duplicates: dups,
	}, nil
}

// lookupSidecarPathByCID is the fallback that LookupPathByCID falls into
// when the token isn't a known root alias. Used to resolve poster/backdrop
// CIDs, which are stored as values pointing at sidecar files (posterPath /
// backdropPath) rather than registered as aliases of the file itself.
//
// O(n) — walks every root. Acceptable because (a) sidecar lookups are rare
// vs. file CID lookups and (b) the alternative is registering sidecars in
// the reverse index, which conflates "this CID is the file" with "this CID
// is an image associated with the file."
func (c *Client) lookupSidecarPathByCID(token string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hashIDs, err := c.getAllHashIDsInternal(ctx)
	if err != nil {
		return "", err
	}

	for _, hashID := range hashIDs {
		meta, err := c.getMetadataFlatInternal(ctx, hashID)
		if err != nil || len(meta) == 0 {
			continue
		}
		if meta["poster"] == token {
			if p := meta["posterPath"]; p != "" {
				return normalizeFilesRelativePath(p), nil
			}
		}
		if meta["backdrop"] == token {
			if p := meta["backdropPath"]; p != "" {
				return normalizeFilesRelativePath(p), nil
			}
		}
	}
	return "", nil
}
