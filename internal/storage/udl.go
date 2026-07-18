package storage

// User Data Layer (UDL) storage.
//
// Holds per-user, identity-signed records keyed by (uid, cid, key) — the
// subjective/per-user state that METADATA_KEYS.md rule #4 bars from the shared
// /file/{cid} hash (likes, ratings, watch progress, local paths). Each record
// is an opaque, client-signed (and, for private-tier keys, client-encrypted)
// CBOR blob; meta-core stores it verbatim and never needs the account key.
//
// Convergence is version-gated: a write is accepted only if its `version`
// strictly exceeds the stored one (a per-cell high-water-mark, replacing the
// op-log merge that used to live in meta-watch's store.rs).
//
// Redis model (all under the literal "udl:" prefix, mirroring the "file:" one):
//
//	udl:rec:<uid>/<cid>/<key>       HASH { record, version, ts, value? }
//	udl:idx:user:<uid>:key:<key>    SET of <cid>   -> active_for_user_key (My List, Continue Watching)
//	udl:idx:user:<uid>:cid:<cid>    SET of <key>   -> list_for_user_cid
//	udl:idx:cid:<cid>:key:<key>     SET of <uid>   -> aggregate
//	udl:idx:cid:<cid>:users         SET of <uid>   -> uids_for_cid
//
// The `value` hash field is the plaintext JSON scalar for PUBLIC keys only
// (like/rating), used for aggregation; it is absent for private-tier keys
// (whose real value rides encrypted inside `record`) and for tombstones.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// UDLCell is a decoded (uid, cid, key) record cell.
type UDLCell struct {
	Record   string // base64 CBOR, opaque + signed
	Version  int64
	Ts       int64
	Value    string // plaintext JSON scalar, public keys only
	HasValue bool
}

// UDLUserKeyEntry is one row of active_for_user_key (all cids for a uid+key).
type UDLUserKeyEntry struct {
	Cid     string `json:"cid"`
	Record  string `json:"record"`
	Version int64  `json:"version"`
	Ts      int64  `json:"ts"`
}

// UDLUserCidEntry is one row of list_for_user_cid (all keys for a uid+cid).
type UDLUserCidEntry struct {
	Key     string `json:"key"`
	Record  string `json:"record"`
	Version int64  `json:"version"`
	Ts      int64  `json:"ts"`
}

func udlRecKey(uid, cid, key string) string {
	return fmt.Sprintf("udl:rec:%s/%s/%s", uid, cid, key)
}
func udlIdxUserKey(uid, key string) string { return fmt.Sprintf("udl:idx:user:%s:key:%s", uid, key) }
func udlIdxUserCid(uid, cid string) string { return fmt.Sprintf("udl:idx:user:%s:cid:%s", uid, cid) }
func udlIdxCidKey(cid, key string) string  { return fmt.Sprintf("udl:idx:cid:%s:key:%s", cid, key) }
func udlIdxCidUsers(cid string) string      { return fmt.Sprintf("udl:idx:cid:%s:users", cid) }

// udlUpsertScript performs the version-gated upsert + index maintenance
// atomically. Accepts (returns 1) only when the incoming version strictly
// exceeds the stored one; otherwise returns 0 (stale).
//
//	KEYS: 1=cell 2=idx:user:key 3=idx:user:cid 4=idx:cid:key 5=idx:cid:users
//	ARGV: 1=version 2=ts 3=record 4=value 5=hasValue("1"/"0")
//	      6=cid 7=key 8=uid
var udlUpsertScript = redis.NewScript(`
local stored = redis.call('HGET', KEYS[1], 'version')
if stored and tonumber(stored) >= tonumber(ARGV[1]) then
  return 0
end
redis.call('HSET', KEYS[1], 'version', ARGV[1], 'ts', ARGV[2], 'record', ARGV[3])
if ARGV[5] == '1' then
  redis.call('HSET', KEYS[1], 'value', ARGV[4])
else
  redis.call('HDEL', KEYS[1], 'value')
end
redis.call('SADD', KEYS[2], ARGV[6])
redis.call('SADD', KEYS[3], ARGV[7])
redis.call('SADD', KEYS[4], ARGV[8])
redis.call('SADD', KEYS[5], ARGV[8])
return 1
`)

// UDLUpsertIfNewer stores a record cell iff version strictly exceeds the stored
// one. publicValue/hasValue carry the plaintext JSON scalar for public keys
// (used by aggregation); pass hasValue=false for private-tier keys and
// tombstones. Returns accepted=false when the write is stale.
func (c *Client) UDLUpsertIfNewer(uid, cid, key string, version, ts int64, record, publicValue string, hasValue bool) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	keys := []string{
		c.buildKey(udlRecKey(uid, cid, key)),
		c.buildKey(udlIdxUserKey(uid, key)),
		c.buildKey(udlIdxUserCid(uid, cid)),
		c.buildKey(udlIdxCidKey(cid, key)),
		c.buildKey(udlIdxCidUsers(cid)),
	}
	hv := "0"
	if hasValue {
		hv = "1"
	}
	res, err := udlUpsertScript.Run(ctx, c.client, keys, version, ts, record, publicValue, hv, cid, key, uid).Result()
	if err != nil {
		return false, fmt.Errorf("udl upsert failed: %w", err)
	}
	n, _ := res.(int64)
	return n == 1, nil
}

// UDLGetCell returns the record cell for (uid, cid, key). exists is false when
// no record has ever been written for the cell.
func (c *Client) UDLGetCell(uid, cid, key string) (UDLCell, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return UDLCell{}, false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.udlReadCell(ctx, udlRecKey(uid, cid, key))
}

// udlReadCell HMGETs a cell hash. Caller holds the lock. exists is false when
// the version field is absent (i.e. the hash does not exist).
func (c *Client) udlReadCell(ctx context.Context, recKey string) (UDLCell, bool, error) {
	vals, err := c.client.HMGet(ctx, c.buildKey(recKey), "record", "version", "ts", "value").Result()
	if err != nil {
		return UDLCell{}, false, fmt.Errorf("hmget failed: %w", err)
	}
	// vals: [record, version, ts, value] — nil for absent fields.
	if len(vals) < 4 || vals[1] == nil {
		return UDLCell{}, false, nil
	}
	cell := UDLCell{}
	if s, ok := vals[0].(string); ok {
		cell.Record = s
	}
	if s, ok := vals[1].(string); ok {
		cell.Version, _ = strconv.ParseInt(s, 10, 64)
	}
	if s, ok := vals[2].(string); ok {
		cell.Ts, _ = strconv.ParseInt(s, 10, 64)
	}
	if s, ok := vals[3].(string); ok {
		cell.Value = s
		cell.HasValue = true
	}
	return cell, true, nil
}

// UDLListUserKey returns every cell for (uid, key) across all cids — the
// projection behind active_for_user_key (My List, Continue Watching). Includes
// tombstones; the caller decodes each record and filters.
func (c *Client) UDLListUserKey(uid, key string) ([]UDLUserKeyEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cids, err := c.client.SMembers(ctx, c.buildKey(udlIdxUserKey(uid, key))).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}
	out := make([]UDLUserKeyEntry, 0, len(cids))
	for _, cid := range cids {
		cell, ok, err := c.udlReadCell(ctx, udlRecKey(uid, cid, key))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, UDLUserKeyEntry{Cid: cid, Record: cell.Record, Version: cell.Version, Ts: cell.Ts})
	}
	return out, nil
}

// UDLListUserCid returns every cell for (uid, cid) across all keys — the
// projection behind list_for_user_cid.
func (c *Client) UDLListUserCid(uid, cid string) ([]UDLUserCidEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	keys, err := c.client.SMembers(ctx, c.buildKey(udlIdxUserCid(uid, cid))).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}
	out := make([]UDLUserCidEntry, 0, len(keys))
	for _, key := range keys {
		cell, ok, err := c.udlReadCell(ctx, udlRecKey(uid, cid, key))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, UDLUserCidEntry{Key: key, Record: cell.Record, Version: cell.Version, Ts: cell.Ts})
	}
	return out, nil
}

// UDLCidUsers returns every uid that has any record for cid — uids_for_cid.
func (c *Client) UDLCidUsers(cid string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	uids, err := c.client.SMembers(ctx, c.buildKey(udlIdxCidUsers(cid))).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}
	return uids, nil
}

// UDLPublicValues returns the live plaintext public JSON scalar values for
// (cid, key) across all uids — the input to aggregation. Cells without a value
// field (private-tier or tombstoned) are skipped.
func (c *Client) UDLPublicValues(cid, key string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	uids, err := c.client.SMembers(ctx, c.buildKey(udlIdxCidKey(cid, key))).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		v, err := c.client.HGet(ctx, c.buildKey(udlRecKey(uid, cid, key)), "value").Result()
		if err == redis.Nil {
			continue // private-tier or tombstoned
		}
		if err != nil {
			return nil, fmt.Errorf("hget failed: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}
