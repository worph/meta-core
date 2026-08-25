package storage

// Account-scoped UDL administration: what a uid holds, and removing all of it.
//
// Both operations enumerate the same thing — every `udl:rec:<uid>/<cid>/<key>`
// cell — because there is no per-user cid index to consult. The Redis model
// (see udl.go) indexes user+key → cids and user+cid → keys, which answer
// "what is in My List", not "everything this account ever wrote". Adding a
// third index would put a write on every UDL upsert to serve two admin calls,
// so these SCAN instead and accept being O(keyspace).
//
// That cost is why UDLAllUserStats exists as a bulk call rather than a per-uid
// one: the accounts page needs a number for every account, and MATCH filters
// server-side without indexing, so N per-uid calls would walk the keyspace N
// times. One walk buckets them all.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// UDLUserStats counts what one account holds. Records is the number of cells,
// including tombstones — a retracted rating is still a row this account owns
// and still something a purge has to remove.
type UDLUserStats struct {
	Records int `json:"records"`
	Cids    int `json:"cids"`
	Keys    int `json:"keys"`
}

// udlScanBatch is the SCAN COUNT hint, matching the rest of this package.
const udlScanBatch = 1000

// safeUIDForPattern rejects anything that would change the meaning of a SCAN
// glob or reach across the key structure. meta-core's uids are alphanumeric
// (identity.validUID), so this only ever fires on a hostile or malformed one —
// but it fires here, in the layer that builds the pattern, rather than relying
// on every caller having validated first.
func safeUIDForPattern(uid string) error {
	if uid == "" {
		return fmt.Errorf("empty uid")
	}
	if strings.ContainsAny(uid, "*?[]\\/:") {
		return fmt.Errorf("uid contains characters that are not valid in a uid: %q", uid)
	}
	return nil
}

// splitUDLRecSuffix splits the "<uid>/<cid>/<key>" tail of a udl:rec key.
//
// SplitN with a limit of 3 so a key containing a slash survives intact. cids
// never contain one; keys are a controlled vocabulary today, but a namespaced
// one arriving later must not silently truncate here — a truncated key would
// make the purge SREM the wrong index member and leave the real one behind.
func splitUDLRecSuffix(suffix string) (uid, cid, key string, ok bool) {
	parts := strings.SplitN(suffix, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// scanKeys walks the keyspace for pattern and returns every match. Caller holds
// the lock.
//
// SCAN can return the same key more than once across cursor iterations (it
// guarantees no misses, not no duplicates), so callers that count must
// de-duplicate — both callers below collect into maps or sets for that reason.
func (c *Client) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var (
		out    []string
		cursor uint64
	)
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, udlScanBatch).Result()
		if err != nil {
			return nil, fmt.Errorf("scan %q: %w", pattern, err)
		}
		out = append(out, keys...)
		cursor = next
		if cursor == 0 {
			return out, nil
		}
	}
}

// UDLUserCells returns every (cid, key) pair this account holds a cell for.
// De-duplicated, since SCAN may repeat a key.
func (c *Client) UDLUserCells(uid string) (map[string]string, error) {
	// Keyed by the full "cid/key" tail so the pair stays a pair; the value is
	// the cid, which the purge needs grouped.
	if err := safeUIDForPattern(uid); err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prefix := c.buildKey("udl:rec:")
	keys, err := c.scanKeys(ctx, prefix+uid+"/*")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(keys))
	for _, full := range keys {
		gotUID, cid, key, ok := splitUDLRecSuffix(strings.TrimPrefix(full, prefix))
		// The glob can only match this uid, but a uid that is a prefix of
		// another would still be caught by the '/' in the pattern; check anyway
		// so a malformed key can never be attributed to the wrong account.
		if !ok || gotUID != uid {
			continue
		}
		out[cid+"/"+key] = cid
	}
	return out, nil
}

// UDLAllUserStats counts every account's cells in one keyspace walk.
//
// Returns only accounts that actually hold records; a uid absent from the map
// holds none. Callers render a zero for those rather than treating it as an
// error — an account created a minute ago legitimately has nothing.
func (c *Client) UDLAllUserStats() (map[string]UDLUserStats, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	prefix := c.buildKey("udl:rec:")
	keys, err := c.scanKeys(ctx, prefix+"*")
	if err != nil {
		return nil, err
	}

	// Sets rather than counters: SCAN may hand back the same key twice, which
	// would otherwise inflate every number on the page.
	type acc struct {
		cells map[string]struct{}
		cids  map[string]struct{}
		keys  map[string]struct{}
	}
	byUID := make(map[string]*acc)
	for _, full := range keys {
		uid, cid, key, ok := splitUDLRecSuffix(strings.TrimPrefix(full, prefix))
		if !ok {
			continue
		}
		a := byUID[uid]
		if a == nil {
			a = &acc{
				cells: make(map[string]struct{}),
				cids:  make(map[string]struct{}),
				keys:  make(map[string]struct{}),
			}
			byUID[uid] = a
		}
		a.cells[cid+"/"+key] = struct{}{}
		a.cids[cid] = struct{}{}
		a.keys[key] = struct{}{}
	}

	out := make(map[string]UDLUserStats, len(byUID))
	for uid, a := range byUID {
		out[uid] = UDLUserStats{Records: len(a.cells), Cids: len(a.cids), Keys: len(a.keys)}
	}
	return out, nil
}

// UDLPurgeUser removes every trace of one account from the User Data Layer:
// its cells, its two user-scoped indexes, and its membership in the shared
// cid-scoped indexes. Returns what was removed.
//
// The shared indexes are the part that is easy to miss and expensive to leave.
// `udl:idx:cid:<cid>:key:<key>` and `udl:idx:cid:<cid>:users` are keyed by cid,
// not by uid, so a purge that only deleted `udl:rec:<uid>/*` would leave this
// uid listed as an opinion-holder on every title it ever touched. Aggregation
// then HGETs a cell that no longer exists — UDLPublicValues skips it, so the
// rating average stays right, but uids_for_cid keeps naming a deleted account
// forever and every aggregate read pays for the lookup.
//
// Idempotent: purging an account with nothing stored is a no-op returning
// zeroes, so a retry after a partial failure is safe.
func (c *Client) UDLPurgeUser(uid string) (UDLUserStats, error) {
	cells, err := c.UDLUserCells(uid)
	if err != nil {
		return UDLUserStats{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return UDLUserStats{}, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	stats := UDLUserStats{Records: len(cells)}
	distinctCids := make(map[string]struct{})
	distinctKeys := make(map[string]struct{})

	pipe := c.client.Pipeline()
	for cidKey, cid := range cells {
		key := strings.TrimPrefix(cidKey, cid+"/")
		distinctCids[cid] = struct{}{}
		distinctKeys[key] = struct{}{}
		pipe.Del(ctx, c.buildKey(udlRecKey(uid, cid, key)))
		pipe.SRem(ctx, c.buildKey(udlIdxCidKey(cid, key)), uid)
	}
	for cid := range distinctCids {
		pipe.SRem(ctx, c.buildKey(udlIdxCidUsers(cid)), uid)
	}
	stats.Cids = len(distinctCids)
	stats.Keys = len(distinctKeys)

	// The two user-scoped indexes are enumerable directly, and are dropped
	// whole rather than member-by-member. Scanning them separately also catches
	// index entries whose cell is already gone — exactly the state a previously
	// half-finished purge leaves behind.
	idxPrefix := c.buildKey("udl:idx:user:") + uid + ":"
	idxKeys, err := c.scanKeys(ctx, idxPrefix+"*")
	if err != nil {
		return UDLUserStats{}, err
	}
	for _, k := range idxKeys {
		pipe.Del(ctx, k)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return UDLUserStats{}, fmt.Errorf("purge pipeline: %w", err)
	}
	return stats, nil
}
