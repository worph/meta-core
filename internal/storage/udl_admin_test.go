package storage

import (
	"testing"
)

// seedCell writes one live UDL cell the way a real client would.
func seedCell(t *testing.T, c *Client, uid, cid, key string) {
	t.Helper()
	if ok, err := c.UDLUpsertIfNewer(uid, cid, key, 1, 100, "rec", "", false, false); err != nil || !ok {
		t.Fatalf("seed %s/%s/%s: ok=%v err=%v", uid, cid, key, ok, err)
	}
}

// The purge has to reach the cid-scoped indexes, not just the user's own rows.
//
// This is the failure that motivated the whole endpoint: deleting an account
// while leaving `udl:idx:cid:<cid>:users` intact means every aggregate read on
// every title that account ever touched keeps naming a uid nobody can resolve,
// forever — and the rows are unreachable through any API, so nothing will ever
// clean them up.
func TestUDLPurgeUser_RemovesCellsAndBothIndexFamilies(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "cidOne", "like")
	seedCell(t, c, "zAlice", "cidOne", "rating")
	seedCell(t, c, "zAlice", "cidTwo", "seek")

	stats, err := c.UDLPurgeUser("zAlice")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if stats.Records != 3 || stats.Cids != 2 || stats.Keys != 3 {
		t.Fatalf("stats = %+v, want 3 records / 2 cids / 3 keys", stats)
	}

	// Cells gone.
	if _, ok, err := c.UDLGetCell("zAlice", "cidOne", "like"); err != nil || ok {
		t.Fatalf("cell survived the purge: ok=%v err=%v", ok, err)
	}
	// User-scoped indexes gone.
	if entries, err := c.UDLListUserKey("zAlice", "like"); err != nil || len(entries) != 0 {
		t.Fatalf("user+key index survived: %d entries, err=%v", len(entries), err)
	}
	if entries, err := c.UDLListUserCid("zAlice", "cidOne"); err != nil || len(entries) != 0 {
		t.Fatalf("user+cid index survived: %d entries, err=%v", len(entries), err)
	}
	// Cid-scoped index gone — the one that is easy to miss.
	for _, cid := range []string{"cidOne", "cidTwo"} {
		uids, err := c.UDLCidUsers(cid)
		if err != nil {
			t.Fatalf("cid users %s: %v", cid, err)
		}
		for _, u := range uids {
			if u == "zAlice" {
				t.Fatalf("%s still lists the deleted account in uids_for_cid", cid)
			}
		}
	}
}

// Purging one account must not touch another's rows, including on a cid they
// both have an opinion about — where the two share an index set and a careless
// DEL of the whole key would take the survivor's membership with it.
func TestUDLPurgeUser_LeavesOtherAccountsIntact(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "shared", "rating")
	seedCell(t, c, "zBob", "shared", "rating")

	if _, err := c.UDLPurgeUser("zAlice"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	if _, ok, err := c.UDLGetCell("zBob", "shared", "rating"); err != nil || !ok {
		t.Fatalf("bob's cell must survive alice's purge: ok=%v err=%v", ok, err)
	}
	uids, err := c.UDLCidUsers("shared")
	if err != nil {
		t.Fatalf("cid users: %v", err)
	}
	if len(uids) != 1 || uids[0] != "zBob" {
		t.Fatalf("uids_for_cid = %v, want exactly [zBob]", uids)
	}
	entries, err := c.UDLListUserKey("zBob", "rating")
	if err != nil || len(entries) != 1 {
		t.Fatalf("bob's user+key index must survive: %d entries, err=%v", len(entries), err)
	}
}

// A uid that is a prefix of another must not drag its neighbour's rows out.
// The key layout separates them with '/' and ':', so this passes only as long
// as the scan patterns keep those separators.
func TestUDLPurgeUser_DoesNotMatchUIDsByPrefix(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "cidOne", "like")
	seedCell(t, c, "zAliceExtra", "cidOne", "like")

	stats, err := c.UDLPurgeUser("zAlice")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if stats.Records != 1 {
		t.Fatalf("purged %d records, want exactly alice's 1", stats.Records)
	}
	if _, ok, err := c.UDLGetCell("zAliceExtra", "cidOne", "like"); err != nil || !ok {
		t.Fatalf("zAliceExtra's cell must survive: ok=%v err=%v", ok, err)
	}
}

// Tombstoned cells still belong to the account. They are absent from the
// user+key index by design (see udl.go), so a purge that enumerated *that*
// index instead of the cells would leave every retracted rating behind — the
// rows most likely to have accumulated over an account's life.
func TestUDLPurgeUser_RemovesTombstonedCellsToo(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "cidOne", "like")
	if ok, err := c.UDLUpsertIfNewer("zAlice", "cidOne", "like", 2, 200, "rec-tomb", "", false, true); err != nil || !ok {
		t.Fatalf("tombstone: ok=%v err=%v", ok, err)
	}
	// Precondition: the tombstone left the cell but dropped it from the index.
	if entries, _ := c.UDLListUserKey("zAlice", "like"); len(entries) != 0 {
		t.Fatalf("precondition: tombstone should be out of the user+key index")
	}

	stats, err := c.UDLPurgeUser("zAlice")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if stats.Records != 1 {
		t.Fatalf("purged %d records, want the 1 tombstoned cell", stats.Records)
	}
	if _, ok, _ := c.UDLGetCell("zAlice", "cidOne", "like"); ok {
		t.Fatal("tombstoned cell survived the purge")
	}
}

// Retry-safe: the delete handler purges before removing the key, so a caller
// that retries after a partial failure runs this a second time.
func TestUDLPurgeUser_IsIdempotent(t *testing.T) {
	c, _ := newTestClient(t, "")
	seedCell(t, c, "zAlice", "cidOne", "like")

	if _, err := c.UDLPurgeUser("zAlice"); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	stats, err := c.UDLPurgeUser("zAlice")
	if err != nil {
		t.Fatalf("second purge must be a no-op, got: %v", err)
	}
	if stats.Records != 0 || stats.Cids != 0 {
		t.Fatalf("second purge = %+v, want zeroes", stats)
	}
}

func TestUDLPurgeUser_RefusesGlobInjection(t *testing.T) {
	c, _ := newTestClient(t, "")
	seedCell(t, c, "zAlice", "cidOne", "like")

	// "*" would otherwise match every account in the keyspace — a wildcard
	// purge dressed up as a uid.
	for _, uid := range []string{"*", "z*", "", "z:Alice", "z/Alice"} {
		if _, err := c.UDLPurgeUser(uid); err == nil {
			t.Errorf("uid %q must be refused before it reaches a SCAN pattern", uid)
		}
	}
	if _, ok, _ := c.UDLGetCell("zAlice", "cidOne", "like"); !ok {
		t.Fatal("a refused purge must not have deleted anything")
	}
}

func TestUDLAllUserStats_CountsEachAccountSeparately(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "cidOne", "like")
	seedCell(t, c, "zAlice", "cidOne", "rating")
	seedCell(t, c, "zAlice", "cidTwo", "like")
	seedCell(t, c, "zBob", "cidOne", "seek")

	stats, err := c.UDLAllUserStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := stats["zAlice"]; got.Records != 3 || got.Cids != 2 || got.Keys != 2 {
		t.Fatalf("alice = %+v, want 3 records / 2 cids / 2 keys", got)
	}
	if got := stats["zBob"]; got.Records != 1 || got.Cids != 1 || got.Keys != 1 {
		t.Fatalf("bob = %+v, want 1/1/1", got)
	}
	// An account with nothing stored is simply absent — the caller renders 0.
	if _, present := stats["zNobody"]; present {
		t.Fatal("an account with no records must not appear in the map")
	}
}

// The stats and the purge must agree: the number shown in the delete
// confirmation is the number of rows the delete then removes.
func TestUDLAllUserStats_AgreesWithWhatPurgeRemoves(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedCell(t, c, "zAlice", "cidOne", "like")
	seedCell(t, c, "zAlice", "cidTwo", "rating")
	seedCell(t, c, "zAlice", "cidTwo", "seek")

	before, err := c.UDLAllUserStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	purged, err := c.UDLPurgeUser("zAlice")
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if before["zAlice"] != purged {
		t.Fatalf("stats said %+v but purge removed %+v", before["zAlice"], purged)
	}
}

// The key prefix (multi-tenant Redis) must not leak into the parsed uid, or
// every account on a prefixed node would be reported under a mangled name and
// the purge would find nothing.
func TestUDLAdmin_RespectsKeyPrefix(t *testing.T) {
	c, _ := newTestClient(t, "tenantA:")

	seedCell(t, c, "zAlice", "cidOne", "like")

	stats, err := c.UDLAllUserStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := stats["zAlice"]; got.Records != 1 {
		t.Fatalf("stats under a prefix = %+v, want 1 record for zAlice", stats)
	}
	if s, err := c.UDLPurgeUser("zAlice"); err != nil || s.Records != 1 {
		t.Fatalf("purge under a prefix: %+v err=%v", s, err)
	}
}

func TestSplitUDLRecSuffix_KeepsSlashesInTheKey(t *testing.T) {
	// A namespaced key arriving later must not be truncated: the purge SREMs
	// `udl:idx:cid:<cid>:key:<key>`, so a half-read key removes the wrong
	// member and leaves the real one behind.
	uid, cid, key, ok := splitUDLRecSuffix("zAlice/cidOne/ns/sub:key")
	if !ok || uid != "zAlice" || cid != "cidOne" || key != "ns/sub:key" {
		t.Fatalf("got (%q,%q,%q,%v)", uid, cid, key, ok)
	}
	for _, bad := range []string{"", "zAlice", "zAlice/cidOne", "zAlice//key", "/cid/key"} {
		if _, _, _, ok := splitUDLRecSuffix(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}
