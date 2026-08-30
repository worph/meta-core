package storage

import (
	"context"
	"sort"
	"testing"
)

// Tests for incremental search-index maintenance (search_dirty.go).
//
// The dirty set is derived state on the hot write path, which makes its failure
// mode the dangerous kind: a MISSING mark doesn't error, it just leaves the
// search index quietly serving a stale copy of that record until someone
// triggers a full rebuild. So the property worth pinning is coverage — every
// path that changes what a search would match must mark — and that is what the
// bulk of these tests assert, one write path at a time.
//
// The second property is that a delete is a mark too, not an unmark: the
// refresh has to LEARN the record is gone in order to evict it. Marking on
// delete is counter-intuitive enough to be worth a test of its own.

func dirtyMembers(t *testing.T, c *Client) []string {
	t.Helper()
	got, err := c.client.SMembers(context.Background(), c.buildDirtyKey()).Result()
	if err != nil {
		t.Fatalf("smembers dirty: %v", err)
	}
	sort.Strings(got)
	return got
}

func clearDirty(t *testing.T, c *Client) {
	t.Helper()
	if err := c.client.Del(context.Background(), c.buildDirtyKey()).Err(); err != nil {
		t.Fatalf("del dirty: %v", err)
	}
}

// Every write path must mark. A path that doesn't leaves the index serving a
// stale record with no error anywhere.
func TestDirty_EveryWritePathMarks(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		cases := []struct {
			name  string
			id    string
			write func(id string) error
		}{
			{"SetMetadataFlat", "r-set", func(id string) error {
				return c.SetMetadataFlat(id, map[string]string{"title": "A"})
			}},
			{"SetProperty", "r-prop", func(id string) error {
				return c.SetProperty(id, "title", "B")
			}},
			{"MergeMetadataFlat", "r-merge", func(id string) error {
				_, err := c.MergeMetadataFlat(id, map[string]string{"title": "C"})
				return err
			}},
			{"AddToSet", "r-add", func(id string) error {
				_, err := c.AddToSet(id, "tags", "x")
				return err
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				clearDirty(t, c)
				if err := tc.write(tc.id); err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				got := dirtyMembers(t, c)
				if len(got) != 1 || got[0] != tc.id {
					t.Fatalf("%s must mark %q dirty, got %v", tc.name, tc.id, got)
				}
			})
		}
	})
}

// RemoveFromSet's non-empty branch changes a field's VALUE without changing the
// field-name index, so it is the one path that a mark riding on field-index
// maintenance would have missed. Pinned explicitly for that reason.
func TestDirty_RemoveFromSetMarksEvenWhenTheFieldSurvives(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const id = "r-rem"
		for _, v := range []string{"x", "y"} {
			if _, err := c.AddToSet(id, "tags", v); err != nil {
				t.Fatalf("AddToSet: %v", err)
			}
		}
		clearDirty(t, c)

		if _, err := c.RemoveFromSet(id, "tags", "x"); err != nil {
			t.Fatalf("RemoveFromSet: %v", err)
		}
		// `tags` still exists (y remains) — no field-index change, but the
		// searchable value changed.
		if got := dirtyMembers(t, c); len(got) != 1 || got[0] != id {
			t.Fatalf("a value-only change must still mark dirty, got %v", got)
		}
	})
}

// A delete MARKS rather than unmarks: the refresh must read the record, find it
// gone, and evict it. Unmarking would leave a deleted record answering searches
// until the next full rebuild.
func TestDirty_DeletesMarkSoTheRefreshCanEvict(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const id = "r-del"
		if err := c.SetMetadataFlat(id, map[string]string{"title": "Doomed"}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}
		clearDirty(t, c)

		if _, err := c.DeleteMetadata(id); err != nil {
			t.Fatalf("DeleteMetadata: %v", err)
		}
		if got := dirtyMembers(t, c); len(got) != 1 || got[0] != id {
			t.Fatalf("a delete must mark dirty so the index evicts it, got %v", got)
		}

		// …and the incremental read reports it as gone rather than as an error
		// or an empty-but-present tuple.
		tuples, gone, err := c.GetSearchIndexTuplesFor([]string{id})
		if err != nil {
			t.Fatalf("GetSearchIndexTuplesFor: %v", err)
		}
		if len(tuples) != 0 {
			t.Fatalf("a deleted record must produce no tuple, got %d", len(tuples))
		}
		if len(gone) != 1 || gone[0] != id {
			t.Fatalf("a deleted record must be reported gone, got %v", gone)
		}
	})
}

// The drain is destructive so concurrent refreshes stay disjoint and a record
// isn't re-read every tick forever. The caller owns what it drained.
func TestDirty_DrainIsDestructive(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		clearDirty(t, c)
		c.MarkDirty("a", "b", "c")

		ids, more, err := c.DrainDirty()
		if err != nil {
			t.Fatalf("DrainDirty: %v", err)
		}
		sort.Strings(ids)
		if len(ids) != 3 {
			t.Fatalf("expected 3 drained ids, got %v", ids)
		}
		if more {
			t.Fatal("a fully-drained set must not report more")
		}
		if got := dirtyMembers(t, c); len(got) != 0 {
			t.Fatalf("drain must empty the set, got %v", got)
		}

		// A second drain is empty, not an error — the steady state when nothing
		// has been written since the last tick.
		ids, _, err = c.DrainDirty()
		if err != nil || len(ids) != 0 {
			t.Fatalf("draining an empty set must be a clean no-op, got %v / %v", ids, err)
		}
	})
}

// The incremental read must produce tuples indistinguishable from the full
// rebuild's — same haystack, same fields, same exclusions. A drift here would
// make a record's matchability depend on which path happened to index it, which
// is invisible until someone searches for the one term that differs.
func TestDirty_IncrementalTuplesMatchTheFullRebuild(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const id = "r-parity"
		if err := c.SetMetadataFlat(id, map[string]string{
			"title":                   "Parity Release",
			"fileName":                "parity.s01e01.mkv",
			"fileType":                "video",
			"categories/XXX":          "true",
			"categories/newznab/5070": "true",
		}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		full := tuplesByHashID(t, c)[id]
		incr, gone, err := c.GetSearchIndexTuplesFor([]string{id})
		if err != nil {
			t.Fatalf("GetSearchIndexTuplesFor: %v", err)
		}
		if len(gone) != 0 || len(incr) != 1 {
			t.Fatalf("expected exactly one live tuple, got %d live / %d gone", len(incr), len(gone))
		}

		if incr[0].Haystack != full.Haystack {
			t.Errorf("haystack drift:\n incremental %q\n full        %q", incr[0].Haystack, full.Haystack)
		}
		if len(incr[0].Fields) != len(full.Fields) {
			t.Errorf("field-count drift: incremental %d, full %d", len(incr[0].Fields), len(full.Fields))
		}
		for k, v := range full.Fields {
			if incr[0].Fields[k] != v {
				t.Errorf("field %q drift: incremental %q, full %q", k, incr[0].Fields[k], v)
			}
		}
		// The index projection must apply on both paths.
		if _, ok := incr[0].Fields["categories/newznab/5070"]; ok {
			t.Error("the incremental path must honour the index exclusion list too")
		}
		if _, ok := incr[0].Fields["categories/XXX"]; !ok {
			t.Error("the incremental path must keep the adult-filter category")
		}
	})
}
