package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// SchemaVersion is the Redis-layout version this build of meta-core expects.
// Distinct from the snapshot manifest version (which is on-disk format);
// this number describes the in-memory Redis schema — primarily, "are roots
// UUIDs or content-hash tokens, and do reverse-index keys cid:* exist?"
//
// Bump only when a Redis-layout change can't be detected from the data
// itself or healed by the auto-alias hooks. Each bump is a flag day in
// alpha: operators wipe and restart.
const SchemaVersion = 2

// SchemaVersionKey is the sentinel under which we record SchemaVersion in
// Redis. Checked on every meta-core boot.
const SchemaVersionKey = "meta-core:schema-version"

// EnsureSchemaVersion verifies the Redis store matches the expected schema
// version. Called once at startup, before any other reads/writes.
//
// Behavior:
//   - sentinel present and matches expected → OK
//   - sentinel missing AND file:__index__ empty → fresh store, stamp sentinel
//   - sentinel missing AND file:__index__ non-empty → legacy data, refuse
//   - sentinel present but mismatched → refuse, point operator at clean:all
//
// Refusal is hard-fail; the caller is expected to log.Fatalf and exit.
// The error message names the wipe remediation because in alpha there
// is no automated migration — operators bring up a clean store.
func (c *Client) EnsureSchemaVersion(expected int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(SchemaVersionKey)
	have, err := c.client.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	if err == redis.Nil {
		// Sentinel absent. Fresh store iff there's no file:__index__ membership.
		n, sErr := c.client.SCard(ctx, c.buildIndexKey()).Result()
		if sErr != nil && sErr != redis.Nil {
			return fmt.Errorf("scard file:__index__: %w", sErr)
		}
		if n > 0 {
			return fmt.Errorf(
				"redis has %d indexed roots but no %q sentinel — this is alpha "+
					"data from before the UUID-rooted schema landed. Run "+
					"`pnpm run clean:all` (or `docker exec metacore-app redis-cli FLUSHDB`) "+
					"to wipe and restart, or downgrade meta-core to a compatible build",
				n, SchemaVersionKey)
		}
		if err := c.client.Set(ctx, key, strconv.Itoa(expected), 0).Err(); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	}

	if have == strconv.Itoa(expected) {
		return nil
	}
	return fmt.Errorf(
		"redis schema version is %q, this build of meta-core expects %d. "+
			"Run `pnpm run clean:all` to wipe and restart, or use a meta-core "+
			"build that matches the existing data",
		have, expected)
}
