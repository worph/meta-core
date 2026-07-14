package storage

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
)

// newTestClient spins an in-memory Redis and returns a connected Client with
// the given namespace prefix. The server is torn down with the test.
func newTestClient(t *testing.T, prefix string) (*Client, *miniredis.Miniredis) {
	t.Helper()

	s := miniredis.RunT(t)
	c := NewClient(prefix)
	if err := c.Connect("redis://" + s.Addr()); err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, s
}

// TestClearAllMetadata_PurgesCIDReverseIndex is the regression test for the
// leak: ClearAllMetadata used to delete every file:<root>/* key and the
// file:__index__ set, but left the cid:<cid> → root reverse-index entries
// behind. Those stale aliases are not cosmetic — ResolveRoot consults them, so
// a surviving entry silently redirects a later write into a deleted root.
func TestClearAllMetadata_PurgesCIDReverseIndex(t *testing.T) {
	for _, prefix := range []string{"", "mm:"} {
		name := "no-prefix"
		if prefix != "" {
			name = "prefixed"
		}
		t.Run(name, func(t *testing.T) {
			c, s := newTestClient(t, prefix)

			// Two records, each with several CID aliases (a midhash plus
			// siblings) — exactly what the watcher produces.
			aliases := map[string][]string{}
			for i, f := range []string{"watch/a.mkv", "watch/b.mkv"} {
				uuid, err := c.Mint(f, int64(100+i), int64(1000+i))
				if err != nil {
					t.Fatalf("mint: %v", err)
				}
				cids := []string{
					"bafkmid" + uuid,
					"bafkfull" + uuid,
				}
				for _, cidStr := range cids {
					if err := c.AddAlias(uuid, cidStr); err != nil {
						t.Fatalf("add alias %s: %v", cidStr, err)
					}
				}
				aliases[uuid] = cids
			}

			// An entry already orphaned by a previous (buggy) clear: a
			// reverse-index key whose root no longer exists. The sweep is
			// expected to collect it too.
			if err := s.Set(c.buildCIDIndexKey("bafkorphan"), "01OLDDEADROOT"); err != nil {
				t.Fatalf("seed orphan: %v", err)
			}

			// Sanity: the aliases resolve before the clear.
			for uuid, cids := range aliases {
				for _, cidStr := range cids {
					if got := c.ResolveRoot(cidStr); got != uuid {
						t.Fatalf("pre-clear ResolveRoot(%s) = %q, want %q", cidStr, got, uuid)
					}
				}
			}

			deleted, err := c.ClearAllMetadata()
			if err != nil {
				t.Fatalf("ClearAllMetadata: %v", err)
			}
			// deletedCount counts files, not keys.
			if deleted != 2 {
				t.Errorf("deletedCount = %d, want 2 (files, not keys)", deleted)
			}

			// No reverse-index key may survive — not for the cleared records,
			// not the pre-existing orphan.
			if leaked := s.Keys(); len(leaked) != 0 {
				t.Errorf("keys survived the clear: %v", leaked)
			}

			// And the aliases must no longer resolve: ResolveRoot returns the
			// hash unchanged when there is no index entry.
			for _, cids := range aliases {
				for _, cidStr := range cids {
					if got := c.ResolveRoot(cidStr); got != cidStr {
						t.Errorf("post-clear ResolveRoot(%s) = %q, want the hash unchanged (stale alias)", cidStr, got)
					}
				}
			}
			if got, err := c.GetByCID("bafkorphan"); err != nil || got != "" {
				t.Errorf("post-clear GetByCID(bafkorphan) = %q (err %v), want \"\"", got, err)
			}
		})
	}
}

// TestClearAllMetadata_LeavesForeignKeysAlone guards the sweep's blast radius:
// it must key off buildCIDIndexKey (namespace-aware), not a hardcoded "cid:",
// so a clear on a prefixed client cannot reach another namespace's index.
func TestClearAllMetadata_LeavesForeignKeysAlone(t *testing.T) {
	c, s := newTestClient(t, "mm:")

	uuid, err := c.Mint("watch/a.mkv", 100, 1000)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := c.AddAlias(uuid, "bafkmine"); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	// Another namespace's reverse-index entry, plus an unrelated key.
	if err := s.Set("other:cid:bafktheirs", "01THEIRROOT"); err != nil {
		t.Fatalf("seed foreign cid: %v", err)
	}
	if err := s.Set("unrelated", "keep-me"); err != nil {
		t.Fatalf("seed unrelated: %v", err)
	}

	if _, err := c.ClearAllMetadata(); err != nil {
		t.Fatalf("ClearAllMetadata: %v", err)
	}

	for _, k := range []string{"other:cid:bafktheirs", "unrelated"} {
		if !s.Exists(k) {
			t.Errorf("clear deleted foreign key %q — the cid sweep is not namespace-aware", k)
		}
	}
	if s.Exists(c.buildCIDIndexKey("bafkmine")) {
		t.Errorf("own reverse-index entry survived the clear")
	}
}
