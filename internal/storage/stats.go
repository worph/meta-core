package storage

import (
	"context"
	"fmt"
	"time"
)

// CountRoots returns the number of metadata roots in the file index.
//
// SCARD, not SMEMBERS: GetAllHashIDs ships every member over the wire (~3MB
// and rising on an 88k-record box) just so the caller can take len(). A
// dashboard tile that polls needs the number, never the list.
func (c *Client) CountRoots() (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n, err := c.client.SCard(ctx, c.buildIndexKey()).Result()
	if err != nil {
		return 0, fmt.Errorf("scard failed: %w", err)
	}
	return n, nil
}

// CountKeys returns DBSIZE — every key in the Redis database, not just the
// ones meta-core owns. It is the denominator for "how much of Redis is
// records": ~3.1M keys against ~89k roots means the flat per-field keys
// (file:{root}/{field}) dominate, which is expected.
func (c *Client) CountKeys() (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n, err := c.client.DBSize(ctx).Result()
	if err != nil {
		return 0, fmt.Errorf("dbsize failed: %w", err)
	}
	return n, nil
}
