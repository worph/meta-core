package storage

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/redis/go-redis/v9"
)

// The per-record field-name index.
//
// A record is stored as one flat Redis STRING per leaf
// (`file:<uuid>/<field>`), and that shape is deliberate — it is what lets
// keyspace notifications fire per *field*, so meta-fuse / meta-editor /
// meta-search can react to one field changing instead of diffing a whole hash
// (see docs/project-architecture/persistent-storage.md, and
// dev/test/suites/flat-keys.bats which asserts records are never hashes).
//
// The cost of that shape was that reading one record meant asking Redis "which
// keys start with `file:<uuid>/`?", and Redis has no prefix index — `SCAN
// MATCH` walks the ENTIRE keyspace. On a real store (445k keys, ~36 fields per
// record) a single record read cost ~670 SCAN round-trips and ~0.5s of Redis
// CPU; SCAN accounted for ~97.5% of all Redis CPU time on the box. That sat on
// the byte path for every poster meta-share served, which is how it surfaced.
//
// So we keep the flat keys and index their *names*:
//
//	file:<uuid>/__fields__   SET{ "filePath", "sizeByte", "cids/bafk…", … }
//
// A read becomes SMEMBERS + a chunked MGET — two round-trips instead of ~670.
// This mirrors `file:__index__` (the set of every root) one level down, and the
// bulk reads already worked this way: `GetTuplesForAllFiles` composes its keys
// from the index rather than scanning per root.
//
// **The set is derived, never authoritative.** The flat keys remain the source
// of truth. A record whose set is missing (anything written before this landed)
// falls back to the old SCAN and populates the set on the way out, so existing
// deployments heal themselves on first touch with no migration and no
// SchemaVersion bump — which matters, because a bump is a flag day that makes
// operators wipe Redis (see schema_version.go).
//
// The consistency argument that makes a derived index safe here is meta-core's
// single-writer rule: leader election publishes an empty `redisUrl` on purpose
// (internal/leader/election.go), so nothing outside this process can write a
// `file:` key behind the index's back.

// FieldsField is the reserved field name holding a record's field-name index.
//
// It is a Redis SET living at `file:<uuid>/__fields__`, i.e. inside the same
// prefix as the record's own fields. That keeps every existing sweep correct
// for free — `DeleteMetadata`, `DeleteRoot` and `ClearAllMetadata` all
// SCAN+DEL `file:<uuid>/*` and therefore drop the index along with the record.
// The flip side is that every reader which enumerates that prefix has to skip
// it, exactly as they already skip `file:__index__`.
const FieldsField = "__fields__"

// mgetBatch caps how many keys go into one MGET. Redis limits arguments per
// command; 1000 is the batch size `GetTuplesForAllFiles` already uses.
const mgetBatch = 1000

// buildFieldsKey constructs the key for a record's field-name index set.
func (c *Client) buildFieldsKey(hashID string) string {
	return c.buildKeyPrefix(hashID) + FieldsField
}

// isReservedFieldName reports whether a field name is bookkeeping rather than
// metadata, and so must never be returned to an API caller, exported into a
// snapshot, or folded into the search haystack.
func isReservedFieldName(field string) bool {
	return field == FieldsField
}

// readMetadataLocked returns a record's full flat metadata. Caller must hold
// c.mu (either mode).
//
// Fast path: SMEMBERS the field index, then MGET those keys. Fallback: the
// legacy full-keyspace SCAN, whose result is used to populate the index so the
// next read takes the fast path.
//
// Returns (nil, nil) for a record with no fields — callers treat that as 404.
func (c *Client) readMetadataLocked(ctx context.Context, hashID string) (map[string]string, error) {
	fields, err := c.client.SMembers(ctx, c.buildFieldsKey(hashID)).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("smembers fields: %w", err)
	}

	if len(fields) > 0 {
		return c.mgetFieldsLocked(ctx, hashID, fields)
	}

	// No index yet: a record written before the index existed, or one that
	// genuinely has no fields. Fall back to the scan, then backfill.
	result, scanned, err := c.scanMetadataLocked(ctx, hashID)
	if err != nil {
		return nil, err
	}
	if len(scanned) > 0 {
		// Best-effort: a failed backfill costs the next read a scan, it does
		// not make the read wrong. Never fail the caller's read for it.
		if err := c.client.SAdd(ctx, c.buildFieldsKey(hashID), toAny(scanned)...).Err(); err != nil {
			logFieldIndexBackfillFailure(hashID, err)
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// mgetFieldsLocked reads the named fields of a record in chunked MGETs.
//
// A field named in the index whose key is gone reads back as nil and is simply
// skipped: a *stale* index entry is harmless. (A *missing* one would silently
// drop data, which is why every write path maintains the set.)
func (c *Client) mgetFieldsLocked(ctx context.Context, hashID string, fields []string) (map[string]string, error) {
	prefix := c.buildKeyPrefix(hashID)

	// Keep the reserved name out of the key list AND out of the result — it is
	// a SET, so MGET would error on it in real Redis rather than return nil.
	wanted := make([]string, 0, len(fields))
	for _, f := range fields {
		if !isReservedFieldName(f) {
			wanted = append(wanted, f)
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}

	result := make(map[string]string, len(wanted))
	for start := 0; start < len(wanted); start += mgetBatch {
		end := start + mgetBatch
		if end > len(wanted) {
			end = len(wanted)
		}
		chunk := wanted[start:end]
		keys := make([]string, len(chunk))
		for i, f := range chunk {
			keys[i] = prefix + f
		}
		values, err := c.client.MGet(ctx, keys...).Result()
		if err != nil {
			return nil, fmt.Errorf("mget failed: %w", err)
		}
		for i, v := range values {
			if s, ok := v.(string); ok {
				result[chunk[i]] = s
			}
		}
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// scanMetadataLocked is the pre-index read: SCAN the record's key prefix, then
// MGET. Retained as the fallback for un-indexed records and as the source for
// the backfill. Returns the metadata plus the field names it saw (which
// includes fields whose value came back nil, so the index stays complete).
func (c *Client) scanMetadataLocked(ctx context.Context, hashID string) (map[string]string, []string, error) {
	prefix := c.buildKeyPrefix(hashID)
	result := make(map[string]string)
	var fields []string

	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, nil, fmt.Errorf("scan failed: %w", err)
		}

		if len(keys) > 0 {
			wanted := make([]string, 0, len(keys))
			for _, key := range keys {
				field := strings.TrimPrefix(key, prefix)
				if isReservedFieldName(field) {
					continue
				}
				fields = append(fields, field)
				wanted = append(wanted, key)
			}
			if len(wanted) > 0 {
				values, err := c.client.MGet(ctx, wanted...).Result()
				if err != nil {
					return nil, nil, fmt.Errorf("mget failed: %w", err)
				}
				for i, key := range wanted {
					if s, ok := values[i].(string); ok {
						result[strings.TrimPrefix(key, prefix)] = s
					}
				}
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return result, fields, nil
}

// addFieldsToPipe records field names in the index as part of an existing
// pipeline, so a multi-field write stays one round-trip.
func (c *Client) addFieldsToPipe(ctx context.Context, pipe redis.Pipeliner, hashID string, fields []string) {
	names := filterReserved(fields)
	if len(names) == 0 {
		return
	}
	pipe.SAdd(ctx, c.buildFieldsKey(hashID), toAny(names)...)
}

// addFieldLocked records a single field name in the index. Caller must hold
// c.mu. Best-effort by the same argument as the backfill: the flat key is the
// source of truth, and a lost index entry is repaired by the next scan
// fallback — so it must not fail the write it accompanies.
func (c *Client) addFieldLocked(ctx context.Context, hashID, field string) {
	if isReservedFieldName(field) {
		return
	}
	if err := c.client.SAdd(ctx, c.buildFieldsKey(hashID), field).Err(); err != nil {
		logFieldIndexWriteFailure(hashID, field, err)
	}
}

// removeFieldLocked drops a field name from the index. Caller must hold c.mu.
//
// A stale entry left behind by a failure is harmless (the MGET reads nil and
// skips it), so this is best-effort too.
func (c *Client) removeFieldLocked(ctx context.Context, hashID, field string) {
	if isReservedFieldName(field) {
		return
	}
	if err := c.client.SRem(ctx, c.buildFieldsKey(hashID), field).Err(); err != nil {
		logFieldIndexWriteFailure(hashID, field, err)
	}
}

// fieldsWithPrefixLocked returns the record's indexed field names under a
// field-name prefix (e.g. "cids/"), falling back to a SCAN when the record has
// no index yet. Caller must hold c.mu.
func (c *Client) fieldsWithPrefixLocked(ctx context.Context, hashID, fieldPrefix string) ([]string, error) {
	fields, err := c.client.SMembers(ctx, c.buildFieldsKey(hashID)).Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("smembers fields: %w", err)
	}
	if len(fields) == 0 {
		// Un-indexed record: scan the narrower `<prefix><fieldPrefix>*` glob
		// rather than the whole record, and do not backfill from it — a
		// partial view must never be mistaken for a complete index.
		return c.scanFieldsWithPrefixLocked(ctx, hashID, fieldPrefix)
	}

	var out []string
	for _, f := range fields {
		if isReservedFieldName(f) {
			continue
		}
		if strings.HasPrefix(f, fieldPrefix) {
			out = append(out, strings.TrimPrefix(f, fieldPrefix))
		}
	}
	return out, nil
}

func (c *Client) scanFieldsWithPrefixLocked(ctx context.Context, hashID, fieldPrefix string) ([]string, error) {
	prefix := c.buildKeyPrefix(hashID) + fieldPrefix
	var out []string
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan fields: %w", err)
		}
		for _, k := range keys {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return out, nil
}

// recordHasFieldsLocked reports whether a record still has at least one
// property key. Caller must hold c.mu.
//
// `__fields__` must not count — it lives under the record's own prefix, so a
// naive "does any `file:<id>/*` key exist?" probe would answer yes for a record
// whose every property has been deleted, and the root could never be
// de-indexed.
func (c *Client) recordHasFieldsLocked(ctx context.Context, hashID string) (bool, error) {
	n, err := c.client.SCard(ctx, c.buildFieldsKey(hashID)).Result()
	if err != nil && err != redis.Nil {
		return false, fmt.Errorf("scard fields: %w", err)
	}
	if n > 0 {
		return true, nil
	}

	// Either genuinely empty, or an un-indexed record from before the index
	// existed — the scan is the only way to tell those apart.
	prefix := c.buildKeyPrefix(hashID)
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return false, fmt.Errorf("scan failed: %w", err)
		}
		for _, k := range keys {
			if !isReservedFieldName(strings.TrimPrefix(k, prefix)) {
				return true, nil
			}
		}
		cursor = next
		if cursor == 0 {
			return false, nil
		}
	}
}

// splitRecordKey decomposes a full Redis key into (hashID, field) when it is a
// record property key. Reports ok=false for anything else — the index set
// itself, `file:__index__`, and any key outside the `file:` namespace.
//
// This is what lets the raw KV escape hatch (SetRawValue / DeleteRawKey) keep
// the index honest without its callers knowing the layout.
func (c *Client) splitRecordKey(key string) (hashID, field string, ok bool) {
	rest := key
	if c.prefix != "" {
		var found bool
		rest, found = strings.CutPrefix(rest, c.prefix)
		if !found {
			return "", "", false
		}
	}
	rest, found := strings.CutPrefix(rest, "file:")
	if !found {
		return "", "", false
	}
	hashID, field, found = strings.Cut(rest, "/")
	if !found || hashID == "" || field == "" {
		// `file:__index__` has no slash and lands here.
		return "", "", false
	}
	if isReservedFieldName(field) {
		return "", "", false
	}
	return hashID, field, true
}

// The index is derived and self-healing, so a write failure degrades
// performance (the next read falls back to a scan and re-populates) rather
// than correctness. Log it rather than failing the caller's write — but log it
// loudly, because a *persistent* failure means every read is scanning again.
func logFieldIndexWriteFailure(hashID, field string, err error) {
	log.Printf("[Storage] field index update failed for %s/%s: %v (reads fall back to SCAN)", hashID, field, err)
}

func logFieldIndexBackfillFailure(hashID string, err error) {
	log.Printf("[Storage] field index backfill failed for %s: %v (reads keep falling back to SCAN)", hashID, err)
}

func filterReserved(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !isReservedFieldName(f) {
			out = append(out, f)
		}
	}
	return out
}

func toAny(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
