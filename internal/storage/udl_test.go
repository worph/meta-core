package storage

import (
	"testing"
)

// A tombstone must leave the cell in place but drop the cid from the user+key
// index.
//
// The cell has to stay: it *is* the CRDT state, and deleting it would let an
// older write resurrect the value the tombstone retracted. The index entry has
// to go: `active_for_user_key` returns every member of that set, so a set that
// only ever grows means each My List / Continue Watching read carries every
// title the user has ever un-liked or finished — with its full record blob —
// forever. The reader filters them out again, so the cost is invisible until
// the account is old.
func TestUDLUpsert_TombstonePrunesUserKeyIndexButKeepsCell(t *testing.T) {
	c, _ := newTestClient(t, "")

	const (
		uid = "zAlice"
		cid = "bagcsaaalocator"
		key = "seek"
	)

	// A live position: indexed, readable.
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 100, "rec-v1", "", false, false); err != nil || !ok {
		t.Fatalf("live upsert: ok=%v err=%v", ok, err)
	}
	entries, err := c.UDLListUserKey(uid, key)
	if err != nil {
		t.Fatalf("active_for_user_key: %v", err)
	}
	if len(entries) != 1 || entries[0].Cid != cid {
		t.Fatalf("live position should be indexed, got %+v", entries)
	}

	// Finishing it tombstones the seek.
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 2, 200, "rec-v2-tombstone", "", false, true); err != nil || !ok {
		t.Fatalf("tombstone upsert: ok=%v err=%v", ok, err)
	}

	entries, err = c.UDLListUserKey(uid, key)
	if err != nil {
		t.Fatalf("active_for_user_key after tombstone: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("tombstoned cid must leave the user+key index, got %+v", entries)
	}

	// ...but the cell itself survives, at the tombstone's version.
	cell, exists, err := c.UDLGetCell(uid, cid, key)
	if err != nil {
		t.Fatalf("get cell: %v", err)
	}
	if !exists {
		t.Fatal("tombstone must not delete the cell — an older write could then resurrect the value")
	}
	if cell.Version != 2 || cell.Record != "rec-v2-tombstone" {
		t.Fatalf("cell should hold the tombstone record at v2, got %+v", cell)
	}

	// And the high-water mark still rejects the superseded write.
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 300, "rec-v1-replay", "", false, false); err != nil || ok {
		t.Fatalf("stale replay must be rejected: ok=%v err=%v", ok, err)
	}

	// Re-watching re-establishes the cell and puts it back in the index.
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 3, 400, "rec-v3", "", false, false); err != nil || !ok {
		t.Fatalf("re-establish upsert: ok=%v err=%v", ok, err)
	}
	entries, err = c.UDLListUserKey(uid, key)
	if err != nil {
		t.Fatalf("active_for_user_key after re-establish: %v", err)
	}
	if len(entries) != 1 || entries[0].Cid != cid {
		t.Fatalf("re-established cid must return to the index, got %+v", entries)
	}
}
