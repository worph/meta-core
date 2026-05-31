package storage

import (
	"context"
	"fmt"
)

// MigrateDualRoots was a one-shot repair for the "dual-root" pattern in the
// legacy storage model: /meta/{hash} write handlers that didn't resolve
// CID → UUID first created a parallel midhash-rooted entry alongside the
// watcher's UUID root.
//
// That pattern is now structurally impossible:
//   - writes resolve via ResolveRoot before landing (see handlers), and
//   - CIDs are stored as a bare-CID key-set (cids/<cid>) keyed off opaque
//     UUID roots, with the reverse index cid:<cid> → uuid as the single
//     resolution path.
//
// The legacy on-disk shape this migration targeted (midhash-rooted entries,
// <algo>:<value> tokens, named midhash256 fields) no longer exists, and the
// cut-over wipes Redis, so there is nothing to migrate. The function is kept
// as a no-op so the admin endpoint and its wrappers continue to compile and
// respond.
func (c *Client) MigrateDualRoots(ctx context.Context) (int, error) {
	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}
	return 0, nil
}

// MigrateDualRootsWithTimeout is a convenience wrapper used by the API
// handler and the startup hook.
func (c *Client) MigrateDualRootsWithTimeout() (int, error) {
	return c.MigrateDualRoots(context.Background())
}

// MigrateDualRootsReport returns a human-readable summary. With the dual-root
// pattern retired (see MigrateDualRoots), this always reports nothing to do.
func (c *Client) MigrateDualRootsReport(ctx context.Context) (string, int, error) {
	return "dual-root migration is obsolete under the bare-CID key-set model; nothing to do", 0, nil
}
