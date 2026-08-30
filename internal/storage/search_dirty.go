package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Incremental search-index maintenance.
//
// # Why this exists
//
// The API's in-memory search index used to be rebuilt in full every 60 s:
// GetSearchIndexTuples does a whole-keyspace SCAN plus batched MGETs, builds an
// intermediate map of every record, then allocates a fresh []SearchTuple — all
// while the previous index is still live serving reads. That cost is O(corpus),
// not O(change): adding 200 records in a minute re-read every one of the other
// ~620 000 field keys to notice them.
//
// Measured on a 19 622-record staging corpus (2026-08-30): SCAN was 57.8 % and
// MGET 19.1 % of ALL Redis CPU, and ~90 % of the MGET calls plus ~48 % of the
// SCAN calls were this loop — roughly 45 % of Redis's total work, spent
// continuously whether or not anything changed. The Go side held 783 MB for
// data Redis stores in 135 MB, largely because of the double copy.
//
// That was tolerable while the corpus was small and mostly static. It stops
// being tolerable now that meta-core is the platform's permanent, additive
// store: every search hit on every gateway is retained forever, so the rebuild
// cost is a ratchet.
//
// # The shape
//
// Every write path marks its record in a dirty set; the refresh drains that set
// and re-reads only those records. Cost becomes O(change).
//
// Marking rides along with the field-index maintenance every write path already
// performs, so there is no new bookkeeping discipline to remember — if you are
// adding a write path, the same line that keeps `__fields__` correct keeps this
// correct.
//
// # Correctness
//
// The drain is a destructive SPOP, so a write that lands *during* a refresh is
// either already in the drained batch or still in the set for the next tick —
// never dropped. The cost of the race is one tick of staleness, which is well
// inside the interval the index was already stale by.
//
// A refresh that fails after draining WOULD lose its batch, so drainDirty's
// caller re-marks on error (see MarkDirty / the API's refreshIncremental).
//
// ⚠ **The destructive drain assumes ONE index consumer per Redis.** Two
// meta-core processes sharing a keyspace would steal each other's marks, and
// each would end up with an index missing whatever the other drained. That is
// fine today — multi-instance leader election was deprecated and the dev/prod
// stacks run a single meta-core — but it is the assumption to revisit first if
// a second instance is ever pointed at the same Redis. A per-consumer dirty key
// (`file:__dirty__:<consumer>`) is the shape that would fix it.
//
// The API's periodic full reconcile (searchIndexReconcileEvery) is the backstop
// for both this and for a write path that forgets to mark: a missed mark costs
// staleness until the next reconcile, never permanent divergence.

// dirtyField is the reserved key holding the set of record ids whose metadata
// changed since the last search-index refresh. It sits under the same `file:`
// namespace as `__index__` and is likewise not a record.
const dirtyField = "file:__dirty__"

// dirtyDrainCap bounds one incremental refresh. A burst larger than this is
// drained over several ticks rather than in one long-held pass — the point of
// the change is to keep any single refresh cheap. Sized well above a normal
// search-persist flush (which writes at most a few hundred records).
const dirtyDrainCap = 5000

// buildDirtyKey constructs the key for the dirty-record set.
func (c *Client) buildDirtyKey() string {
	return c.buildKey(dirtyField)
}

// markDirtyLocked records that hashID's metadata changed. Caller must hold c.mu.
//
// Best-effort by design: a failure here costs one record its incremental
// refresh, and the periodic full rebuild still repairs it. Failing the caller's
// write instead would trade a stale index entry for a lost write, which is
// strictly worse.
func (c *Client) markDirtyLocked(ctx context.Context, hashID string) {
	if hashID == "" {
		return
	}
	if err := c.client.SAdd(ctx, c.buildDirtyKey(), hashID).Err(); err != nil {
		log.Printf("[STORAGE] dirty-mark failed for %s: %v (index repairs on the next full rebuild)", hashID, err)
	}
}

// MarkDirty records that hashID needs re-indexing. Exported for the API layer,
// which re-marks a batch whose refresh failed after the drain.
func (c *Client) MarkDirty(hashIDs ...string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.client == nil || len(hashIDs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	members := make([]interface{}, 0, len(hashIDs))
	for _, h := range hashIDs {
		if h != "" {
			members = append(members, h)
		}
	}
	if len(members) == 0 {
		return
	}
	if err := c.client.SAdd(ctx, c.buildDirtyKey(), members...).Err(); err != nil {
		log.Printf("[STORAGE] dirty re-mark failed for %d records: %v", len(members), err)
	}
}

// DrainDirty removes and returns up to dirtyDrainCap record ids from the dirty
// set. `more` reports whether entries remain, so the caller can schedule
// another pass instead of waiting a full interval to catch up on a burst.
//
// Destructive on purpose: SPOP makes concurrent refreshes disjoint and stops a
// record being re-read every tick forever. The caller owns the drained ids
// until it has indexed them — on failure it must MarkDirty them again.
func (c *Client) DrainDirty() (ids []string, more bool, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	key := c.buildDirtyKey()
	ids, err = c.client.SPopN(ctx, key, dirtyDrainCap).Result()
	if err != nil {
		return nil, false, fmt.Errorf("spop dirty: %w", err)
	}
	if len(ids) < dirtyDrainCap {
		return ids, false, nil
	}
	remaining, err := c.client.SCard(ctx, key).Result()
	if err != nil {
		// We have the batch; not knowing the remainder just means the next
		// tick picks it up on schedule.
		return ids, false, nil
	}
	return ids, remaining > 0, nil
}

// GetSearchIndexTuplesFor reads the search tuples for a specific set of records
// — the incremental counterpart to GetSearchIndexTuples.
//
// Reads go through the per-record field index (GetMetadataFlat), so this is two
// round-trips per record rather than a keyspace scan. That trade is what makes
// it O(change): it beats the full pass whenever the changed set is a small
// fraction of the corpus, and the caller bounds the batch (dirtyDrainCap).
//
// A record present in the dirty set but absent from storage was deleted; it is
// reported in `gone` so the caller can evict it from the index rather than
// leaving a tombstone that still answers searches.
func (c *Client) GetSearchIndexTuplesFor(hashIDs []string) (tuples []SearchTuple, gone []string, err error) {
	if len(hashIDs) == 0 {
		return nil, nil, nil
	}

	excluded := indexExcludePrefixes()
	tuples = make([]SearchTuple, 0, len(hashIDs))

	for _, hashID := range hashIDs {
		fields, ferr := c.GetMetadataFlat(hashID)
		if ferr != nil {
			// Re-mark so the record is retried rather than silently missing
			// from the index until the next full rebuild.
			c.MarkDirty(hashID)
			log.Printf("[STORAGE] incremental index read failed for %s: %v", hashID, ferr)
			continue
		}
		if len(fields) == 0 {
			gone = append(gone, hashID)
			continue
		}
		for field := range fields {
			if hasAnyPrefix(field, excluded) {
				delete(fields, field)
			}
		}
		tuples = append(tuples, SearchTuple{
			HashID:   hashID,
			Haystack: buildHaystack(fields),
			Fields:   fields,
		})
	}
	return tuples, gone, nil
}

// buildHaystack folds a record's searchable text fields into one lowercased
// blob. Shared by the full and incremental index reads so the two can never
// disagree about what is searchable — a drift there would make a record's
// matchability depend on which path happened to index it.
func buildHaystack(fields map[string]string) string {
	var sb strings.Builder
	for _, f := range searchIndexFields {
		if v, ok := fields[f]; ok && v != "" {
			sb.WriteString(strings.ToLower(v))
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
