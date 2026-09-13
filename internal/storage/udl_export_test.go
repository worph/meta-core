package storage

import (
	"context"
	"testing"
)

// The whole-profile export is what cross-instance recovery reads: a box that
// has just imported a secret key knows the uid and nothing else — not which
// cids the profile touched, not which keys. Every one of these tests is about
// that one caller.

func TestUDLAllForUser_ReturnsEveryCellAcrossCidsAndKeys(t *testing.T) {
	c, _ := newTestClient(t, "")

	const uid = "zAlice"
	writes := []struct{ cid, key, rec string }{
		{"bagcsaaaaone", "like", "rec-one-like"},
		{"bagcsaaaaone", "seek", "rec-one-seek"},
		{"bagcsaaaatwo", "rating", "rec-two-rating"},
		{"bagcsaaaatwo", "profile:name", "rec-two-name"},
		{"bagcsaaathree", "watched", "rec-three-watched"},
	}
	for _, w := range writes {
		if ok, err := c.UDLUpsertIfNewer(uid, w.cid, w.key, 1, 100, w.rec, "", false, false); err != nil || !ok {
			t.Fatalf("upsert %s/%s: ok=%v err=%v", w.cid, w.key, ok, err)
		}
	}

	entries, next, err := c.UDLAllForUser(uid, "", 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if next != "" {
		t.Fatalf("export of 5 rows should be complete, got nextCid=%q", next)
	}
	if len(entries) != len(writes) {
		t.Fatalf("want %d rows, got %d: %+v", len(writes), len(entries), entries)
	}
	seen := map[string]string{}
	for _, e := range entries {
		seen[e.Cid+"/"+e.Key] = e.Record
	}
	for _, w := range writes {
		if got := seen[w.cid+"/"+w.key]; got != w.rec {
			t.Errorf("%s/%s: want record %q, got %q", w.cid, w.key, w.rec, got)
		}
	}
}

// The failure this guards is silent and total: a uid whose cids were never
// indexed exports as empty, and recovery reports success having restored
// nothing.
func TestUDLAllForUser_IsEmptyForAnUnknownUid(t *testing.T) {
	c, _ := newTestClient(t, "")

	if ok, err := c.UDLUpsertIfNewer("zAlice", "bagcsaaaaone", "like", 1, 100, "rec", "", false, false); err != nil || !ok {
		t.Fatalf("upsert: ok=%v err=%v", ok, err)
	}
	entries, _, err := c.UDLAllForUser("zBob", "", 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Bob owns nothing; got %+v", entries)
	}
}

// ⚠ The tombstone rule, and the reason the cids index is never pruned.
//
// A tombstone must EXPORT. It is the only thing that can beat the older live
// value the other box still holds at a lower version — drop it from the export
// and an un-liked title comes back from the dead on every peer that already
// saw the `like`. This is the one place the cids index deliberately diverges
// from the user+key index, which does prune.
func TestUDLAllForUser_ExportsTombstones(t *testing.T) {
	c, _ := newTestClient(t, "")

	const (
		uid = "zAlice"
		cid = "bagcsaaaaone"
		key = "like"
	)
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 100, "rec-liked", "", false, false); err != nil || !ok {
		t.Fatalf("live upsert: ok=%v err=%v", ok, err)
	}
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 2, 200, "rec-unliked", "", false, true); err != nil || !ok {
		t.Fatalf("tombstone upsert: ok=%v err=%v", ok, err)
	}

	// The user+key index prunes it — that is what keeps My List short.
	live, err := c.UDLListUserKey(uid, key)
	if err != nil {
		t.Fatalf("active_for_user_key: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("tombstoned cid should leave the user+key index, got %+v", live)
	}

	// The export must still carry it, at the tombstone's version.
	entries, _, err := c.UDLAllForUser(uid, "", 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("tombstone must export, got %+v", entries)
	}
	if entries[0].Record != "rec-unliked" || entries[0].Version != 2 {
		t.Fatalf("export must carry the tombstone, not the superseded value: %+v", entries[0])
	}
}

func TestUDLAllForUser_PagesOnACidBoundaryAndResumes(t *testing.T) {
	c, _ := newTestClient(t, "")

	const uid = "zAlice"
	cids := []string{"bagcsaaaaa1", "bagcsaaaaa2", "bagcsaaaaa3"}
	for _, cid := range cids {
		for _, key := range []string{"like", "seek"} {
			if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 100, "rec-"+cid+"-"+key, "", false, false); err != nil || !ok {
				t.Fatalf("upsert: ok=%v err=%v", ok, err)
			}
		}
	}

	// A cid's keys are never split across a page, so a limit of 1 still yields
	// both of the first cid's rows.
	page1, next1, err := c.UDLAllForUser(uid, "", 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Cid != cids[0] {
		t.Fatalf("page 1 should hold both rows of %s, got %+v", cids[0], page1)
	}
	if next1 != cids[0] {
		t.Fatalf("cursor should name the last cid emitted, want %q got %q", cids[0], next1)
	}

	// Walking the cursor must visit every row exactly once.
	all := append([]UDLUserAllEntry{}, page1...)
	cursor := next1
	for cursor != "" {
		page, next, err := c.UDLAllForUser(uid, cursor, 1)
		if err != nil {
			t.Fatalf("page after %q: %v", cursor, err)
		}
		all = append(all, page...)
		cursor = next
	}
	if len(all) != 6 {
		t.Fatalf("want 6 rows across all pages, got %d: %+v", len(all), all)
	}
	seen := map[string]bool{}
	for _, e := range all {
		k := e.Cid + "/" + e.Key
		if seen[k] {
			t.Fatalf("%s emitted twice across pages", k)
		}
		seen[k] = true
	}
}

// Data written before the cids index existed must still export, or every
// profile that predates this feature silently recovers as empty.
func TestUDLBackfillUserCids_MakesLegacyDataExportable(t *testing.T) {
	c, _ := newTestClient(t, "")

	const (
		uid = "zAlice"
		cid = "bagcsaaaaone"
		key = "like"
	)
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 100, "rec", "", false, false); err != nil || !ok {
		t.Fatalf("upsert: ok=%v err=%v", ok, err)
	}

	// Simulate the pre-index state: the cell and the other indexes exist, the
	// cids set does not.
	ctx := context.Background()
	if err := c.client.Del(ctx, c.buildKey(udlIdxUserCids(uid))).Err(); err != nil {
		t.Fatalf("del cids index: %v", err)
	}
	if entries, _, err := c.UDLAllForUser(uid, "", 0); err != nil || len(entries) != 0 {
		t.Fatalf("precondition: legacy data should export as empty, got %+v err=%v", entries, err)
	}

	added, err := c.UDLBackfillUserCids()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if added != 1 {
		t.Fatalf("want 1 cid backfilled, got %d", added)
	}
	entries, _, err := c.UDLAllForUser(uid, "", 0)
	if err != nil {
		t.Fatalf("export after backfill: %v", err)
	}
	if len(entries) != 1 || entries[0].Cid != cid || entries[0].Key != key {
		t.Fatalf("backfilled data should export, got %+v", entries)
	}

	// Idempotent: a second run adds nothing.
	if added, err := c.UDLBackfillUserCids(); err != nil || added != 0 {
		t.Fatalf("second backfill should be a no-op, added=%d err=%v", added, err)
	}
}

// The backfill splits `<uid>:cid:<cid>` on the FIRST separator, because a uid
// is multibase base58btc and its alphabet has no colon. Splitting on the last
// occurrence — the obvious-looking choice — mis-parses a cid that contains
// ":cid:" in its own name into uid "zAlice:cid:weird", cid "name".
func TestUDLBackfillUserCids_SplitsOnTheFirstSeparator(t *testing.T) {
	c, _ := newTestClient(t, "")

	const uid = "zAlice"
	cid := "weird:cid:name"
	if ok, err := c.UDLUpsertIfNewer(uid, cid, "like", 1, 100, "rec", "", false, false); err != nil || !ok {
		t.Fatalf("upsert: ok=%v err=%v", ok, err)
	}
	ctx := context.Background()
	if err := c.client.Del(ctx, c.buildKey(udlIdxUserCids(uid))).Err(); err != nil {
		t.Fatalf("del cids index: %v", err)
	}
	if _, err := c.UDLBackfillUserCids(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	entries, _, err := c.UDLAllForUser(uid, "", 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(entries) != 1 || entries[0].Cid != cid {
		t.Fatalf("want the cid parsed whole, got %+v", entries)
	}
}
