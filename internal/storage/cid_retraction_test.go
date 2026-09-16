package storage

import (
	"context"
	"testing"
)

// Tests for retracting a `cids/<cid>` key-set member (cid_resolution.go's
// removeAliasLocked, reached through DeleteProperty).
//
// Why this is worth its own file. Adding a member writes THREE keys — the flat
// `file:<uuid>/cids/<cid>`, the `__fields__` index entry, and the reverse alias
// `cid:<cid>` → `<uuid>`. Until removeAliasLocked existed, deleting removed only
// the first two, so a retracted CID kept resolving to the record: the member was
// gone from the document and the claim was not.
//
// That asymmetry had a live consequence. meta-share minted dag-pb roots and
// midhash256s over whatever `archive::pick_media` elected, which on an
// obfuscated posting was an arbitrary RAR volume or a par2 file. Six records on
// watch.nsl.sh carry such members. `METADATA_KEYS.md` §2 defines a `cids/`
// member as a digest of *this record's file* and notes that every member is a
// reverse index back to the record — so those entries assert that an archive
// volume is the episode. Retracting one has to remove the assertion, not just
// the row.

// aliasTarget returns the uuid that cid:<cidStr> currently resolves to, or "".
func aliasTarget(t *testing.T, c *Client, cidStr string) string {
	t.Helper()
	got, err := c.client.Get(context.Background(), c.buildCIDIndexKey(cidStr)).Result()
	if err != nil {
		return ""
	}
	return got
}

func hasField(t *testing.T, c *Client, hashID, field string) bool {
	t.Helper()
	ok, err := c.client.SIsMember(context.Background(), c.buildFieldsKey(hashID), field).Result()
	if err != nil {
		t.Fatalf("sismember: %v", err)
	}
	return ok
}

// The real shape: `…glnrqztt…` is the nzb-release locator whose record published
// a dag-pb root computed over volume 3 of a 35-volume RAR5 set.
const (
	poisonedRoot = "bafybeicicuzdvuztca2hp2g2k7scgr4nnibilzgoppljeprphybqot7v6i"
	goodMidhash  = "bagacbabaed6uiulnv7tscpxne22eztkjvnxg5almn43dpfecppixarfe7xhlu"
)

func TestRetractingACidMemberRemovesTheClaimNotJustTheRow(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const uuid = "01KSBHMTESTRECORD"

		if err := c.SetMetadataFlat(uuid, map[string]string{
			"cids/" + poisonedRoot: "true",
			"cids/" + goodMidhash:  "true",
			"fileType":             "video",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// All three writes landed, and the CID resolves to the record.
		if got := aliasTarget(t, c, poisonedRoot); got != uuid {
			t.Fatalf("alias should point at the record before retraction, got %q", got)
		}
		if !hasField(t, c, uuid, "cids/"+poisonedRoot) {
			t.Fatal("field index should list the member before retraction")
		}
		if got := c.ResolveRoot(poisonedRoot); got != uuid {
			t.Fatalf("ResolveRoot should find the record before retraction, got %q", got)
		}

		if err := c.DeleteProperty(uuid, "cids/"+poisonedRoot); err != nil {
			t.Fatalf("DeleteProperty: %v", err)
		}

		// 1. the flat key
		if v, err := c.client.Get(context.Background(),
			c.buildKeyPrefix(uuid)+"cids/"+poisonedRoot).Result(); err == nil {
			t.Fatalf("flat key should be gone, still %q", v)
		}
		// 2. the field index
		if hasField(t, c, uuid, "cids/"+poisonedRoot) {
			t.Fatal("field index still lists the retracted member")
		}
		// 3. the reverse alias — the one that used to survive
		if got := aliasTarget(t, c, poisonedRoot); got != "" {
			t.Fatalf("reverse alias survived retraction, still points at %q", got)
		}
		if got := c.ResolveRoot(poisonedRoot); got == uuid {
			t.Fatal("a retracted cid must not resolve to the record any more")
		}

		// The siblings are untouched: retraction is surgical, not a purge.
		if got := aliasTarget(t, c, goodMidhash); got != uuid {
			t.Fatalf("an unrelated sibling lost its alias, got %q", got)
		}
		if !hasField(t, c, uuid, "cids/"+goodMidhash) {
			t.Fatal("an unrelated sibling lost its field-index entry")
		}
		if v, err := c.GetProperty(uuid, "fileType"); err != nil || v != "video" {
			t.Fatalf("an unrelated field changed: %q %v", v, err)
		}
	})
}

// Two records can race to claim one cid; addAliasLocked leaves the first
// writer's alias in place. Retracting the loser's member must not unpick the
// winner's index — which is why removeAliasLocked compares before deleting.
func TestRetractingDoesNotStealAnotherRecordsAlias(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const winner = "01KSBHMWINNER"
		const loser = "01KSBHMLOSER"
		const shared = "bafybeiaeb2lqi4z3np3q4tarldtyjp2t5lvnoodmtpvpi2tz73azn5td2m"

		if err := c.SetMetadataFlat(winner, map[string]string{"cids/" + shared: "true"}); err != nil {
			t.Fatalf("seed winner: %v", err)
		}
		if err := c.SetMetadataFlat(loser, map[string]string{"cids/" + shared: "true"}); err != nil {
			t.Fatalf("seed loser: %v", err)
		}
		if got := aliasTarget(t, c, shared); got != winner {
			t.Fatalf("the first writer should own the alias, got %q", got)
		}

		if err := c.DeleteProperty(loser, "cids/"+shared); err != nil {
			t.Fatalf("DeleteProperty: %v", err)
		}

		if got := aliasTarget(t, c, shared); got != winner {
			t.Fatalf("retracting the loser's member stole the winner's alias, now %q", got)
		}
		if hasField(t, c, loser, "cids/"+shared) {
			t.Fatal("the loser's own member should still be gone")
		}
	})
}

// A non-cids field has no alias to clean up, and the delete must not invent one.
func TestRetractingAnOrdinaryFieldTouchesNoAlias(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const uuid = "01KSBHMORDINARY"
		if err := c.SetMetadataFlat(uuid, map[string]string{
			"cids/" + goodMidhash: "true",
			"quality":             "1080p",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := c.DeleteProperty(uuid, "quality"); err != nil {
			t.Fatalf("DeleteProperty: %v", err)
		}
		if got := aliasTarget(t, c, goodMidhash); got != uuid {
			t.Fatalf("deleting an ordinary field disturbed a cid alias, got %q", got)
		}
	})
}
