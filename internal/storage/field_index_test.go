package storage

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// Tests for the per-record field-name index (field_index.go).
//
// The index is what makes a record read O(fields) instead of O(keyspace). It is
// derived state, so the properties worth pinning are: (1) a read served from
// the index returns exactly what a scan would have, (2) a record with no index
// still reads correctly and gets one, and (3) every write path keeps it in
// sync — because a *missing* entry silently drops a field from every
// subsequent read, which is the one failure mode that would be invisible.
//
// Every case runs with and without a namespace prefix, matching clear_test.go.

func forEachPrefix(t *testing.T, fn func(t *testing.T, c *Client)) {
	t.Helper()
	for _, prefix := range []string{"", "mm:"} {
		name := "no-prefix"
		if prefix != "" {
			name = "prefix-" + prefix
		}
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, prefix)
			fn(t, c)
		})
	}
}

func fieldNames(t *testing.T, c *Client, hashID string) []string {
	t.Helper()
	got, err := c.client.SMembers(context.Background(), c.buildFieldsKey(hashID)).Result()
	if err != nil {
		t.Fatalf("smembers fields: %v", err)
	}
	sort.Strings(got)
	return got
}

func mustGetMetadata(t *testing.T, c *Client, hashID string) map[string]string {
	t.Helper()
	got, err := c.GetMetadataFlat(hashID)
	if err != nil {
		t.Fatalf("GetMetadataFlat: %v", err)
	}
	return got
}

// SetMetadataFlat must index every field it writes, and the read must come back
// through the index rather than a scan.
func TestFieldIndex_SetMetadataFlatIndexesFields(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		want := map[string]string{
			"filePath":                      "/files/watch/a.mkv",
			"sizeByte":                      "123",
			"cids/bafkreiaaa":               "true",
			"titles/jpn":                    "ナルト",
			"categories/TV/UHD":             "true",
			"info/files/0/path":             "a.mkv",
			"provenance":                    `{"source":"gateway"}`,
			"plot/eng":                      "a plot with / slashes",
			"stream/3":                      "eng",
			"genres/action":                 "true",
			"originaltitle":                 "Naruto",
			"deeply/nested/field/name/here": "x",
		}
		if err := c.SetMetadataFlat("root1", want); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		if got := mustGetMetadata(t, c, "root1"); !reflect.DeepEqual(got, want) {
			t.Errorf("GetMetadataFlat mismatch\n got %v\nwant %v", got, want)
		}

		// The index must name every field — including the ones containing '/',
		// which is where a naive "split on the first slash" would go wrong.
		wantNames := make([]string, 0, len(want))
		for k := range want {
			wantNames = append(wantNames, k)
		}
		sort.Strings(wantNames)
		if got := fieldNames(t, c, "root1"); !reflect.DeepEqual(got, wantNames) {
			t.Errorf("field index mismatch\n got %v\nwant %v", got, wantNames)
		}
	})
}

// A record written before the index existed has flat keys but no set. The read
// must still be correct, and must leave an index behind so the next read takes
// the fast path. This is the migration path for the ~12k records already on
// disk: no flag day, no SchemaVersion bump.
func TestFieldIndex_LazyBackfillForUnindexedRecord(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		ctx := context.Background()
		prefix := c.buildKeyPrefix("legacy")
		want := map[string]string{
			"filePath":        "/files/watch/old.mkv",
			"sizeByte":        "42",
			"cids/bafkreiold": "true",
		}
		// Write the flat keys directly, bypassing every index-maintaining path
		// — exactly the shape a pre-upgrade store is in.
		for f, v := range want {
			if err := c.client.Set(ctx, prefix+f, v, 0).Err(); err != nil {
				t.Fatalf("seed %s: %v", f, err)
			}
		}
		if names := fieldNames(t, c, "legacy"); len(names) != 0 {
			t.Fatalf("precondition: expected no index, got %v", names)
		}

		if got := mustGetMetadata(t, c, "legacy"); !reflect.DeepEqual(got, want) {
			t.Errorf("scan-fallback read mismatch\n got %v\nwant %v", got, want)
		}

		wantNames := []string{"cids/bafkreiold", "filePath", "sizeByte"}
		if got := fieldNames(t, c, "legacy"); !reflect.DeepEqual(got, wantNames) {
			t.Errorf("backfill mismatch\n got %v\nwant %v", got, wantNames)
		}

		// Second read, now served from the index, must agree with the first.
		if got := mustGetMetadata(t, c, "legacy"); !reflect.DeepEqual(got, want) {
			t.Errorf("indexed read disagrees with scan read\n got %v\nwant %v", got, want)
		}
	})
}

// Each single-field write path must add to the index, and each delete path must
// remove from it. A field written but not indexed vanishes from every later
// read — silently — so this is the property that matters most.
func TestFieldIndex_SingleFieldWritePathsStayInSync(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		if err := c.SetProperty("root2", "title", "Naruto"); err != nil {
			t.Fatalf("SetProperty: %v", err)
		}
		if _, err := c.AddToSet("root2", "tags", "anime"); err != nil {
			t.Fatalf("AddToSet: %v", err)
		}
		if _, err := c.MergeMetadataFlat("root2", map[string]string{"year": "2002"}); err != nil {
			t.Fatalf("MergeMetadataFlat: %v", err)
		}

		want := map[string]string{"title": "Naruto", "tags": "anime", "year": "2002"}
		if got := mustGetMetadata(t, c, "root2"); !reflect.DeepEqual(got, want) {
			t.Errorf("after writes\n got %v\nwant %v", got, want)
		}

		// DeleteProperty drops the key and the index entry.
		if err := c.DeleteProperty("root2", "title"); err != nil {
			t.Fatalf("DeleteProperty: %v", err)
		}
		// RemoveFromSet deletes the key once its last member is gone.
		if _, err := c.RemoveFromSet("root2", "tags", "anime"); err != nil {
			t.Fatalf("RemoveFromSet: %v", err)
		}

		want = map[string]string{"year": "2002"}
		if got := mustGetMetadata(t, c, "root2"); !reflect.DeepEqual(got, want) {
			t.Errorf("after deletes\n got %v\nwant %v", got, want)
		}
		if got := fieldNames(t, c, "root2"); !reflect.DeepEqual(got, []string{"year"}) {
			t.Errorf("index after deletes = %v, want [year]", got)
		}
	})
}

// The raw KV editor can mint or drop a record property out of band. It is the
// one write path whose caller does not know the record layout, so the storage
// layer has to keep the index honest for it.
func TestFieldIndex_RawKeyEditorStaysInSync(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		if err := c.SetRawValue("file:root3/hand-written", "v"); err != nil {
			t.Fatalf("SetRawValue: %v", err)
		}
		if got := fieldNames(t, c, "root3"); !reflect.DeepEqual(got, []string{"hand-written"}) {
			t.Errorf("index after SetRawValue = %v", got)
		}
		if got := mustGetMetadata(t, c, "root3"); !reflect.DeepEqual(got, map[string]string{"hand-written": "v"}) {
			t.Errorf("read after SetRawValue = %v", got)
		}

		if _, err := c.DeleteRawKey("file:root3/hand-written"); err != nil {
			t.Fatalf("DeleteRawKey: %v", err)
		}
		if got := fieldNames(t, c, "root3"); len(got) != 0 {
			t.Errorf("index after DeleteRawKey = %v, want empty", got)
		}

		// Keys outside the record namespace must be left alone entirely.
		if err := c.SetRawValue("cid:bafkreiaaa", "root3"); err != nil {
			t.Fatalf("SetRawValue non-record: %v", err)
		}
		if _, _, ok := c.splitRecordKey(c.buildKey("cid:bafkreiaaa")); ok {
			t.Error("cid: key was treated as a record property")
		}
		if _, _, ok := c.splitRecordKey(c.buildIndexKey()); ok {
			t.Error("file:__index__ was treated as a record property")
		}
	})
}

// Mint and AddAlias write fields through their own pipelines, so they need the
// index too. AddAlias in particular is how `cids/<cid>` members appear — miss
// it and a record's CID key-set becomes invisible to every read.
func TestFieldIndex_MintAndAliasIndexTheirFields(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		uuid, err := c.Mint("/files/watch/x.mkv", 10, 20)
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if got := fieldNames(t, c, uuid); !reflect.DeepEqual(got, []string{"filePath", "mtimeNano", "sizeByte"}) {
			t.Errorf("index after Mint = %v", got)
		}

		if err := c.AddAlias(uuid, "bafkreiaaa"); err != nil {
			t.Fatalf("AddAlias: %v", err)
		}
		got := mustGetMetadata(t, c, uuid)
		if got["cids/bafkreiaaa"] != "true" {
			t.Errorf("alias key-set member missing from read: %v", got)
		}

		// And the CID enumeration, which now reads the index instead of
		// scanning, must see it.
		cids, err := c.cidsForRootLocked(context.Background(), uuid)
		if err != nil {
			t.Fatalf("cidsForRootLocked: %v", err)
		}
		if !reflect.DeepEqual(cids, []string{"bafkreiaaa"}) {
			t.Errorf("cidsForRoot = %v, want [bafkreiaaa]", cids)
		}
	})
}

// The index lives under the record's own prefix, so the "is this record empty?"
// probe would otherwise always find it and the root could never be de-indexed.
func TestFieldIndex_IndexRemoveIfEmptyIgnoresTheIndexItself(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		if err := c.SetProperty("root4", "title", "x"); err != nil {
			t.Fatalf("SetProperty: %v", err)
		}

		removed, err := c.IndexRemoveIfEmpty("root4")
		if err != nil {
			t.Fatalf("IndexRemoveIfEmpty: %v", err)
		}
		if removed {
			t.Error("de-indexed a record that still has a field")
		}

		if err := c.DeleteProperty("root4", "title"); err != nil {
			t.Fatalf("DeleteProperty: %v", err)
		}
		removed, err = c.IndexRemoveIfEmpty("root4")
		if err != nil {
			t.Fatalf("IndexRemoveIfEmpty: %v", err)
		}
		if !removed {
			t.Fatal("record with no fields left was not de-indexed — the field index masked emptiness")
		}
		// ...and the now-empty index key must not be left orphaned.
		n, err := c.client.Exists(context.Background(), c.buildFieldsKey("root4")).Result()
		if err != nil {
			t.Fatalf("exists: %v", err)
		}
		if n != 0 {
			t.Error("orphaned field index left behind after de-indexing")
		}
	})
}

// The same probe must still work for a record that predates the index.
func TestFieldIndex_IndexRemoveIfEmptyHandlesUnindexedRecord(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		ctx := context.Background()
		if err := c.client.Set(ctx, c.buildKeyPrefix("legacy2")+"title", "x", 0).Err(); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := c.IndexAdd("legacy2"); err != nil {
			t.Fatalf("IndexAdd: %v", err)
		}
		removed, err := c.IndexRemoveIfEmpty("legacy2")
		if err != nil {
			t.Fatalf("IndexRemoveIfEmpty: %v", err)
		}
		if removed {
			t.Error("de-indexed an un-indexed record that still has a field")
		}
	})
}

// The reserved name is bookkeeping. It must never reach an API caller as a
// metadata field, and an inbound payload must never be able to overwrite the
// SET with a string — that would break every subsequent read of the record.
func TestFieldIndex_ReservedNameIsNeverMetadata(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		err := c.SetMetadataFlat("root5", map[string]string{
			"title":     "x",
			FieldsField: "malicious",
		})
		if err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		got := mustGetMetadata(t, c, "root5")
		if _, present := got[FieldsField]; present {
			t.Errorf("%s surfaced as a metadata field: %v", FieldsField, got)
		}
		if !reflect.DeepEqual(got, map[string]string{"title": "x"}) {
			t.Errorf("read = %v, want just title", got)
		}

		// The index must still be a SET — i.e. not clobbered by the payload.
		if got := fieldNames(t, c, "root5"); !reflect.DeepEqual(got, []string{"title"}) {
			t.Errorf("index = %v, want [title]", got)
		}

		if err := c.SetProperty("root5", FieldsField, "malicious"); err == nil {
			t.Error("SetProperty accepted the reserved field name")
		}
	})
}

// Deleting a record must take its index with it, or a recreated root would
// inherit phantom field names.
func TestFieldIndex_DeleteMetadataRemovesTheIndex(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		if err := c.SetMetadataFlat("root6", map[string]string{"title": "x", "year": "2002"}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}
		if _, err := c.DeleteMetadata("root6"); err != nil {
			t.Fatalf("DeleteMetadata: %v", err)
		}
		if got := fieldNames(t, c, "root6"); len(got) != 0 {
			t.Errorf("index survived DeleteMetadata: %v", got)
		}
		got, err := c.GetMetadataFlat("root6")
		if err != nil {
			t.Fatalf("GetMetadataFlat: %v", err)
		}
		if got != nil {
			t.Errorf("deleted record still reads back: %v", got)
		}
	})
}

// A stale index entry (name present, key gone) is the benign direction: the
// MGET reads nil and the field is skipped rather than erroring.
func TestFieldIndex_StaleEntryIsHarmless(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		ctx := context.Background()
		if err := c.SetMetadataFlat("root7", map[string]string{"title": "x"}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}
		// Name a field that has no key behind it.
		if err := c.client.SAdd(ctx, c.buildFieldsKey("root7"), "ghost").Err(); err != nil {
			t.Fatalf("sadd ghost: %v", err)
		}

		got := mustGetMetadata(t, c, "root7")
		if !reflect.DeepEqual(got, map[string]string{"title": "x"}) {
			t.Errorf("read = %v, want just title", got)
		}
	})
}

// WarmFieldIndexes is what keeps an upgrade from spending hours reading
// un-indexed records one full-keyspace scan at a time. It must index every
// record in one pass, be idempotent, and leave already-indexed records alone.
func TestFieldIndex_WarmBuildsEveryMissingIndex(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		ctx := context.Background()

		// Two legacy records seeded straight into Redis (no index), plus one
		// written through the normal path (already indexed).
		legacy := map[string]map[string]string{
			"old1": {"filePath": "/a.mkv", "cids/bafkreiaaa": "true"},
			"old2": {"filePath": "/b.mkv", "titles/jpn": "ナルト", "deep/a/b": "x"},
		}
		for root, fields := range legacy {
			for f, v := range fields {
				if err := c.client.Set(ctx, c.buildKeyPrefix(root)+f, v, 0).Err(); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			if err := c.IndexAdd(root); err != nil {
				t.Fatalf("IndexAdd: %v", err)
			}
		}
		if err := c.SetMetadataFlat("fresh", map[string]string{"title": "x"}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		n, err := c.WarmFieldIndexes()
		if err != nil {
			t.Fatalf("WarmFieldIndexes: %v", err)
		}
		if n < 2 {
			t.Errorf("warmed %d records, want at least the 2 legacy ones", n)
		}

		if got := fieldNames(t, c, "old1"); !reflect.DeepEqual(got, []string{"cids/bafkreiaaa", "filePath"}) {
			t.Errorf("old1 index = %v", got)
		}
		if got := fieldNames(t, c, "old2"); !reflect.DeepEqual(got, []string{"deep/a/b", "filePath", "titles/jpn"}) {
			t.Errorf("old2 index = %v", got)
		}
		if got := fieldNames(t, c, "fresh"); !reflect.DeepEqual(got, []string{"title"}) {
			t.Errorf("fresh index disturbed = %v", got)
		}

		// Reads must now agree with what the records actually hold.
		for root, want := range legacy {
			if got := mustGetMetadata(t, c, root); !reflect.DeepEqual(got, want) {
				t.Errorf("%s read = %v, want %v", root, got, want)
			}
		}

		// Idempotent: a second pass changes nothing.
		if _, err := c.WarmFieldIndexes(); err != nil {
			t.Fatalf("second WarmFieldIndexes: %v", err)
		}
		if got := fieldNames(t, c, "old2"); !reflect.DeepEqual(got, []string{"deep/a/b", "filePath", "titles/jpn"}) {
			t.Errorf("old2 index after second warm = %v", got)
		}
	})
}

// The bulk search read enumerates the whole file: keyspace and MGETs what it
// finds. The index keys are SETs living in that same namespace, so it has to
// skip them or the MGET fails with WRONGTYPE and search dies wholesale.
func TestFieldIndex_SearchIndexTuplesSkipTheIndexKeys(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		if err := c.SetMetadataFlat("root8", map[string]string{
			"title":    "Naruto Shippuden",
			"fileName": "naruto.mkv",
		}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		tuples, err := c.GetSearchIndexTuples()
		if err != nil {
			t.Fatalf("GetSearchIndexTuples: %v", err)
		}
		var found bool
		for _, tu := range tuples {
			if tu.HashID != "root8" {
				continue
			}
			found = true
			if _, present := tu.Fields[FieldsField]; present {
				t.Errorf("%s leaked into the search tuple: %v", FieldsField, tu.Fields)
			}
		}
		if !found {
			t.Fatal("record missing from search tuples")
		}
	})
}
