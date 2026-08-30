package storage

import (
	"testing"
)

// Tests for the search-index field projection (GetSearchIndexTuples +
// indexExcludePrefixes).
//
// The projection narrows the in-memory index only — Redis keeps every field and
// every per-record read still returns it. What has to be pinned is the boundary
// between the two, because getting it wrong in either direction is silent:
// exclude too little and the index keeps paying for fields nothing reads;
// exclude too much and a record served *from the index* comes back missing a
// field a downstream consumer depends on.
//
// The specific hazard the default guards is adult-content filtering:
// `categories/XXX` is the server-side enforcement point (meta-search's
// `query_eval::fields_are_adult`, meta-gateway's `cards::fields_are_adult`),
// and `exclude_adult` defaults to ON. Excluding the whole `categories/` family
// would make every index-served record read as non-adult and the filter would
// fail OPEN — so the default must keep `categories/XXX` while dropping the
// reader-less `categories/newznab/` mirror.

func tuplesByHashID(t *testing.T, c *Client) map[string]SearchTuple {
	t.Helper()
	got, err := c.GetSearchIndexTuples()
	if err != nil {
		t.Fatalf("GetSearchIndexTuples: %v", err)
	}
	out := make(map[string]SearchTuple, len(got))
	for _, tu := range got {
		out[tu.HashID] = tu
	}
	return out
}

func TestSearchIndex_ExcludesNewznabCategoriesButKeepsTheRest(t *testing.T) {
	forEachPrefix(t, func(t *testing.T, c *Client) {
		const id = "root-1"
		if err := c.SetMetadataFlat(id, map[string]string{
			"title":                   "Some Release",
			"fileType":                "video",
			"categories/TV/Anime":     "true",
			"categories/XXX":          "true",
			"categories/newznab/5070": "true",
			"categories/newznab/2040": "true",
		}); err != nil {
			t.Fatalf("SetMetadataFlat: %v", err)
		}

		fields := tuplesByHashID(t, c)[id].Fields
		if fields == nil {
			t.Fatal("record missing from the search index")
		}

		for _, excluded := range []string{"categories/newznab/5070", "categories/newznab/2040"} {
			if _, ok := fields[excluded]; ok {
				t.Errorf("%s must not be indexed — it is a reader-less mirror vocabulary", excluded)
			}
		}
		// The load-bearing ones must survive. XXX especially: without it the
		// adult filter fails open on every index-served record.
		for _, kept := range []string{"categories/XXX", "categories/TV/Anime", "title", "fileType"} {
			if _, ok := fields[kept]; !ok {
				t.Errorf("%s must stay in the index", kept)
			}
		}

		// The store itself is untouched — the projection is index-only.
		stored, err := c.GetMetadataFlat(id)
		if err != nil {
			t.Fatalf("GetMetadataFlat: %v", err)
		}
		if _, ok := stored["categories/newznab/5070"]; !ok {
			t.Error("an index-excluded field must still be stored and readable per-record")
		}
	})
}

func TestIndexExcludePrefixes_EnvOverride(t *testing.T) {
	if got := indexExcludePrefixes(); len(got) != 1 || got[0] != "categories/newznab/" {
		t.Fatalf("unset env must yield the default, got %v", got)
	}

	t.Setenv("META_CORE_INDEX_EXCLUDE_PREFIXES", "foo/, bar/ ,")
	got := indexExcludePrefixes()
	if len(got) != 2 || got[0] != "foo/" || got[1] != "bar/" {
		t.Fatalf("override must trim and drop blanks, got %v", got)
	}

	// An explicitly empty value is the escape hatch: index everything.
	t.Setenv("META_CORE_INDEX_EXCLUDE_PREFIXES", "")
	if got := indexExcludePrefixes(); len(got) != 0 {
		t.Fatalf("an empty override must disable exclusion, got %v", got)
	}
}

func TestHasAnyPrefix(t *testing.T) {
	prefixes := []string{"categories/newznab/", "tmp/"}
	cases := map[string]bool{
		"categories/newznab/5070": true,
		"tmp/scratch":             true,
		"categories/XXX":          false,
		"categories/newznab":      false, // the family root is not the mirror
		"title":                   false,
	}
	for field, want := range cases {
		if got := hasAnyPrefix(field, prefixes); got != want {
			t.Errorf("hasAnyPrefix(%q) = %v, want %v", field, got, want)
		}
	}
	if hasAnyPrefix("anything", nil) {
		t.Error("an empty prefix list must exclude nothing")
	}
}
