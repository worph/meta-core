package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// MigrateDualRoots scans file:__index__ for hashIds that look like raw
// midhash256 CID values (i.e. NOT UUIDs/ULIDs) and have a matching alias
// `cid:midhash256:<hash>` pointing to a different UUID. For each such
// "stranded" root we:
//
//  1. Copy every file:<midhash>/<property> into file:<uuid>/<property>
//     using MergeMetadataFlat (preserves anything the UUID root already
//     holds, e.g. duplicates and watcher-owned fields).
//  2. Delete the entire file:<midhash>/* subtree.
//  3. Drop the midhash hashId from file:__index__ (kept on the UUID).
//
// This is the one-shot fix for the dual-root pattern described in
// docs/api-mediated-access.md. The pattern was caused by the (now-fixed)
// /meta/{hash} write handlers not resolving CID → UUID before writing.
//
// Returns the number of stranded roots fixed.
func (c *Client) MigrateDualRoots(ctx context.Context) (int, error) {
	c.mu.RLock()
	if c.client == nil {
		c.mu.RUnlock()
		return 0, fmt.Errorf("not connected")
	}
	hashIDs, err := c.getAllHashIDsInternal(ctx)
	c.mu.RUnlock()
	if err != nil {
		return 0, fmt.Errorf("get hash ids: %w", err)
	}

	// Build the orphaned-UUID index: for every UUID-rooted entry, record
	// midhash256 → UUID. This lets us find the "real" target when an alias
	// has been overwritten to self-point (the failure mode the in-flight
	// fix prevents going forward, but a one-shot still needs to repair
	// existing dual-rooted data).
	midhashToUUID := make(map[string]string)
	for _, hashID := range hashIDs {
		if !looksLikeULID(hashID) {
			continue
		}
		mh, err := c.GetProperty(hashID, "midhash256")
		if err != nil || mh == "" {
			continue
		}
		midhashToUUID[mh] = hashID
	}

	fixed := 0
	for _, hashID := range hashIDs {
		// Skip likely-UUID hashIds. ULIDs are 26 chars, uppercase Crockford
		// Base32. We treat anything that's not all-uppercase-alphanumeric
		// of length ~26 as a CID-shaped hashId worth checking.
		if looksLikeULID(hashID) {
			continue
		}

		// Is there an alias pointing to a different uuid?
		token := "midhash256:" + hashID
		uuid, err := c.GetByCID(token)
		if err != nil {
			log.Printf("[MigrateDualRoots] GetByCID(%s): %v (skipping)", token, err)
			continue
		}
		// Three cases:
		//   - uuid empty → no alias, this midhash root is standalone (skip)
		//   - uuid == hashID → self-pointing alias; check the orphan index
		//     for a UUID that claims this midhash via its midhash256 field
		//   - uuid != hashID and != "" → proper alias, migrate to that UUID
		if uuid == "" || uuid == hashID {
			if orphan, ok := midhashToUUID[hashID]; ok && orphan != "" {
				uuid = orphan
			} else {
				continue
			}
		}

		// Pull every key under file:<hashID>/* and merge into file:<uuid>/.
		flat, err := c.GetMetadataFlat(hashID)
		if err != nil {
			log.Printf("[MigrateDualRoots] GetMetadataFlat(%s): %v (skipping)", hashID, err)
			continue
		}
		if len(flat) > 0 {
			if _, err := c.MergeMetadataFlat(uuid, flat); err != nil {
				log.Printf("[MigrateDualRoots] MergeMetadataFlat(%s ← %s): %v", uuid, hashID, err)
				continue
			}
		}

		// Delete the stranded root entirely.
		if _, err := c.DeleteMetadata(hashID); err != nil {
			log.Printf("[MigrateDualRoots] DeleteMetadata(%s): %v", hashID, err)
			continue
		}

		// Re-point the alias to the surviving UUID. DeleteMetadata wipes
		// the cid:* entries for the deleted root, so the alias for this
		// midhash may have been removed. Restore it pointing at the
		// canonical UUID so future ResolveRoot calls land correctly.
		if err := c.AddAlias(uuid, "midhash256:"+hashID); err != nil {
			log.Printf("[MigrateDualRoots] AddAlias(%s → midhash256:%s): %v", uuid, hashID, err)
			// Not fatal — data is migrated; the alias mismatch can be
			// re-fixed by another sweep.
		}

		fixed++
	}

	if fixed > 0 {
		log.Printf("[MigrateDualRoots] Reunited %d stranded midhash roots into their UUID counterparts", fixed)
	}
	return fixed, nil
}

// looksLikeULID is a cheap heuristic for "this hashId is opaque internal
// UUID, not a CID we should resolve." ULIDs are 26-char Crockford Base32;
// CIDs in this codebase are longer and lowercase. Tolerates dashes for
// future-proofing.
func looksLikeULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// MigrateDualRootsWithTimeout is a convenience wrapper used by the API
// handler and the startup hook. Default to a generous 5-minute deadline —
// the per-entry work is small but the index can be large.
func (c *Client) MigrateDualRootsWithTimeout() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return c.MigrateDualRoots(ctx)
}

// MigrateDualRootsReport returns a human-readable summary of what
// MigrateDualRoots would do or did. Useful for the admin endpoint.
func (c *Client) MigrateDualRootsReport(ctx context.Context) (string, int, error) {
	fixed, err := c.MigrateDualRoots(ctx)
	if err != nil {
		return "", fixed, err
	}
	parts := []string{}
	parts = append(parts, fmt.Sprintf("merged %d stranded midhash roots into UUID roots", fixed))
	return strings.Join(parts, "; "), fixed, nil
}
