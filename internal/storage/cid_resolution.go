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
// flat metadata storage in client.go. The short version:
//
//   - Roots are opaque UUIDv7s — file:<uuid>/<property> for every property.
//     The UUID is an internal implementation detail; the mental model is
//     "N CIDs → one record of keys→metadata".
//   - Every known CID for a file is registered as a reverse-index entry:
//       cid:<bareCid> → <uuid>                (Redis STRING)
//     and as a member of the per-root bare-CID key-set:
//       file:<uuid>/cids/<bareCid> = "true"   (flat key, sentinel value)
//   - A CID is self-describing: its algorithm is recoverable from the CIDv1
//     multicodec (see internal/cid), so there is no per-algorithm field name
//     and no <algo>: token prefix. The canonical externally-visible CID is
//     *derived by rank* from the key-set on read (GetMetadataDocument), never
//     stored.
//
// Auto-alias hooks in SetProperty/SetMetadataFlat/MergeMetadataFlat catch
// any write of a cids/<cid> field and register the reverse-index entry, so
// writers that just emit the key-set members don't need to know about the
// reverse index at all. The hook helpers are *Locked — they assume c.mu is
// already held by the caller, avoiding nested RLock (which can deadlock
// against a waiting writer).

// CIDIndexPrefix is the Redis key prefix for the reverse index. Combined
// with a bare CID, it forms the full key: "cid:bafk…".
const CIDIndexPrefix = "cid:"

// CIDsKeyPrefix is the field-name prefix for the bare-CID key-set. Sibling
// CIDs live as flat keys file:<uuid>/cids/<cid> = "true"; the set IS the
// keyset, the value is the sentinel. Read by GetMetadataDocument (hoisted
// into the typed CIDs array) and by DeleteRoot/DeleteMetadata (to find the
// reverse-index entries that need cleanup).
const CIDsKeyPrefix = "cids/"

// DuplicatesField is the per-root SET key holding additional file paths
// whose content hashes to the same CIDs as this root. The watcher writes
// here when it sees a new path for content that's already indexed; meta-dup
// reads from it.
const DuplicatesField = "duplicates"

// buildCIDIndexKey returns the absolute Redis key for a reverse-index entry.
// cidStr is a bare multibase CIDv1 ("bafk…").
func (c *Client) buildCIDIndexKey(cidStr string) string {
	return c.buildKey(CIDIndexPrefix + cidStr)
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
	c.addFieldsToPipe(ctx, pipe, uuid, []string{"filePath", "sizeByte", "mtimeNano"})
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("mint pipeline: %w", err)
	}
	return uuid, nil
}

// AddAlias registers cidStr as a sibling CID of uuid: it writes the reverse
// index cid:<cidStr> → uuid and the key-set member cids/<cidStr> = "true".
// Idempotent: registering the same CID twice is a no-op.
func (c *Client) AddAlias(uuid, cidStr string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.addAliasLocked(ctx, uuid, cidStr)
}

// addAliasLocked is the lock-free body of AddAlias. The caller must already
// hold c.mu (read or write). Split out so the alias-maintenance hooks
// invoked from SetProperty / SetMetadataFlat / MergeMetadataFlat can reuse
// the work without re-entering the lock — nested RLock can deadlock against
// a waiting writer.
func (c *Client) addAliasLocked(ctx context.Context, uuid, cidStr string) error {
	if uuid == "" || cidStr == "" {
		return fmt.Errorf("uuid and cid are required")
	}

	keysetField := CIDsKeyPrefix + cidStr
	keysetKey := c.buildKeyPrefix(uuid) + keysetField

	// Every branch below writes the key-set member, so index the field name
	// once here — including the two guard branches that skip the alias itself.
	// Missing it would leave `cids/<cid>` invisible to a record read.
	defer c.addFieldLocked(ctx, uuid, keysetField)

	// Self-pointing alias guard. If the caller is trying to register
	// cid:<cid> → <cid> (i.e. the root IS the cid value), that's the
	// dual-root failure mode in disguise — it means somebody wrote
	// /meta/<cid> without first resolving via ResolveRoot. Refuse the alias
	// write; leave any existing alias alone. Still record the key-set
	// membership so the field-level write succeeds.
	if uuid == cidStr {
		return c.client.Set(ctx, keysetKey, "true", 0).Err()
	}

	// Guard against the dual-root pattern: if cid:<cid> already points at a
	// DIFFERENT uuid, don't silently overwrite. Leave the existing alias and
	// just record the key-set membership so it's still discoverable via the
	// root → cids enumeration.
	indexKey := c.buildCIDIndexKey(cidStr)
	existing, err := c.client.Get(ctx, indexKey).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read existing alias: %w", err)
	}
	if existing != "" && existing != uuid {
		return c.client.Set(ctx, keysetKey, "true", 0).Err()
	}

	pipe := c.client.Pipeline()
	pipe.Set(ctx, indexKey, uuid, 0)
	pipe.Set(ctx, keysetKey, "true", 0)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("add alias pipeline: %w", err)
	}
	return nil
}

// ResolveRoot maps a hash supplied by a caller (a meta-sort write, an editor
// lookup, an SSE consumer) to the actual storage root key.
//
// Callers send a bare CID (e.g. the midhash "bafk…" the watcher publishes on
// file:events, or any sibling CID). Roots are kept opaque and every CID is
// aliased into the reverse index:
//
//	cid:bafk… → 01KSBHM*
//
// Without resolution, every /meta/<cid> write creates a parallel cid-rooted
// entry that the watcher's UUID never sees.
//
// Behaviour:
//   - empty hash → empty (caller's problem)
//   - hash matches a reverse-index entry → return UUID
//   - otherwise return hash unchanged (legacy / explicit-UUID writes)
//
// Errors are converted to "no resolution" — if Redis is down or the lookup
// fails we let the caller proceed with the bare hash and surface the Redis
// failure on the actual operation.
func (c *Client) ResolveRoot(hash string) string {
	if hash == "" {
		return hash
	}
	uuid, err := c.GetByCID(hash)
	if err != nil || uuid == "" {
		return hash
	}
	return uuid
}

// GetByCID resolves a bare CID to the UUID of its root. Returns ("", nil)
// when the CID is unknown; the empty string is the "not found" signal rather
// than a typed error so callers can branch without errors.As.
func (c *Client) GetByCID(cidStr string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}
	if cidStr == "" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uuid, err := c.client.Get(ctx, c.buildCIDIndexKey(cidStr)).Result()
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

// cidsForRootLocked returns a root's bare-CID key-set members (with the cids/
// prefix stripped). Caller must hold c.mu.
//
// Reads the field index and filters by prefix; only an un-indexed root falls
// back to a SCAN, and then over the narrower cids/ glob. See field_index.go.
func (c *Client) cidsForRootLocked(ctx context.Context, uuid string) ([]string, error) {
	return c.fieldsWithPrefixLocked(ctx, uuid, CIDsKeyPrefix)
}

// DeleteRoot removes everything keyed off uuid: every file:<uuid>/* property
// (including the cids/<cid> key-set), every reverse-index entry for those
// CIDs, and the index membership. Idempotent — a uuid with no remaining keys
// is a successful no-op.
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

	// 1. Read the cids key-set first — once we delete file:<uuid>/* we lose
	//    the list of reverse-index entries to clean up.
	cids, err := c.cidsForRootLocked(ctx, uuid)
	if err != nil {
		return err
	}

	// 2. Delete every reverse-index entry in one shot.
	if len(cids) > 0 {
		keys := make([]string, 0, len(cids))
		for _, cidStr := range cids {
			keys = append(keys, c.buildCIDIndexKey(cidStr))
		}
		if err := c.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("del cid index: %w", err)
		}
	}

	// 3. Scan and delete every file:<uuid>/* key (cids/<cid> keys included).
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

// maybeAddAliasFromFieldLocked inspects a (field, value) pair written into a
// root. If it's a key-set member — field name starts with "cids/" — it
// registers the implied CID in the reverse index. Writers emit cids/<cid>
// members without knowing about the reverse index; this hook keeps it in
// sync transparently.
//
// Caller must hold c.mu. Errors are intentionally swallowed at the call site
// (logged, not returned): an out-of-sync reverse index is recoverable via a
// sweep, while a failed property write is not.
func (c *Client) maybeAddAliasFromFieldLocked(ctx context.Context, uuid, field, value string) error {
	cidStr := cidFromKeysetField(field)
	if cidStr == "" {
		return nil
	}
	return c.addAliasLocked(ctx, uuid, cidStr)
}

// maybeAddAliasesFromMetadataLocked bulk-applies maybeAddAliasFromFieldLocked
// over every entry in a metadata map. Used by SetMetadataFlat /
// MergeMetadataFlat after the underlying pipeline write succeeds. Caller must
// hold c.mu.
func (c *Client) maybeAddAliasesFromMetadataLocked(ctx context.Context, uuid string, metadata map[string]string) {
	for k := range metadata {
		_ = c.maybeAddAliasFromFieldLocked(ctx, uuid, k, metadata[k])
	}
}

// cidFromKeysetField returns the bare CID encoded in a cids/<cid> field name,
// or "" if the field isn't a key-set member.
func cidFromKeysetField(field string) string {
	if !strings.HasPrefix(field, CIDsKeyPrefix) {
		return ""
	}
	return strings.TrimPrefix(field, CIDsKeyPrefix)
}

// MetadataDocument is the complete state of one file as the public API
// returns it: every flat string field (the cids/<cid> members included),
// plus the CIDs hoisted into a typed array, the canonical CID derived by
// rank, and the duplicates set. Distinct from GetMetadataFlat (which only
// returns the raw string keys).
type MetadataDocument struct {
	Flat       map[string]string `json:"metadata"`
	CIDs       []string          `json:"cids"`
	Canonical  string            `json:"canonical_cid,omitempty"`
	Duplicates []string          `json:"duplicates"`
}

// GetMetadataDocument returns the full document for a root: every string
// property under file:<uuid>/* (cids/<cid> members included in Flat), the
// CIDs hoisted into a typed array, the canonical CID computed by rank, and
// the duplicates SET. Returns nil if the root has no keys at all (deleted or
// never existed).
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

	// Hoist the cids/<cid> key-set members into a typed array and derive the
	// canonical CID by rank. The members remain in Flat too — that's how
	// flat-map consumers (meta-share ingest) see them.
	cids := make([]string, 0)
	canonical := ""
	for k := range flat {
		if !strings.HasPrefix(k, CIDsKeyPrefix) {
			continue
		}
		cidStr := strings.TrimPrefix(k, CIDsKeyPrefix)
		cids = append(cids, cidStr)
		if canonical == "" {
			canonical = cidStr
		} else {
			canonical = cid.Better(canonical, cidStr)
		}
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
		Canonical:  canonical,
		Duplicates: dups,
	}, nil
}
