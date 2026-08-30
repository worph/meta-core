package api

import "testing"

// Tests for the structured-filter matcher that lets meta-core answer an
// id-anchored query from the in-memory search index (search_index.go).
//
// Why this matters beyond "filters work": meta-core is a *coarse pre-filter*
// for meta-gateway, which re-evaluates every record it emits through its own
// `query_eval::record_matches`. So the property that has to hold is that this
// matcher stays a SUPERSET of that one — it may admit records the gateway will
// drop, but it must never drop a record the gateway would have kept. Every
// case below is written from that angle:
//
//   - key-set fields (`genres/Action`) must match a `genres:action` filter, or
//     card-tier records vanish from a genre-scoped query;
//   - a missing field fails CLOSED, matching record_matches;
//   - an empty/unusable filter constrains NOTHING, so a caller that lowers a
//     blank filter gets its results rather than an empty page.

func f(property string, exact bool, values ...string) MetadataFilter {
	return MetadataFilter{Property: property, Values: values, Exact: exact}
}

func TestRecordMatchesFilter(t *testing.T) {
	fields := map[string]string{
		"fileType":               "video",
		"videoType":              "tv,anime",
		"tmdbid":                 "1405",
		"genres/Action":          "true",
		"genres/Science Fiction": "true",
		"source/gateway:nyaa.si": "true",
		"categories/XXX":         "true",
		"retracted":              "false",
	}

	cases := []struct {
		name   string
		filter MetadataFilter
		want   bool
	}{
		// Flat scalar.
		{"flat exact hit", f("fileType", true, "video"), true},
		{"flat exact miss", f("fileType", true, "audio"), false},
		{"flat exact is case-insensitive", f("fileType", true, "VIDEO"), true},

		// csv-set field: each comma piece is compared independently.
		{"csv member hit", f("videoType", true, "anime"), true},
		{"csv member miss", f("videoType", true, "movie"), false},

		// Exact vs contains — the reason Exact exists. A substring match on an
		// identity field admits neighbours and, under a result limit, crowds
		// the real matches off the page.
		{"contains admits a superstring", f("tmdbid", false, "140"), true},
		{"exact rejects a superstring", f("tmdbid", true, "140"), false},
		{"exact accepts the whole value", f("tmdbid", true, "1405"), true},

		// Key-set shape. The stored member is verbatim (`genres/Action`) while
		// query values arrive lowercased, so the case-insensitive sweep is what
		// makes a genre-scoped row work at all.
		{"key-set exact casing", f("genres", true, "Action"), true},
		{"key-set lowercased query", f("genres", true, "action"), true},
		{"key-set with a space", f("genres", true, "science fiction"), true},
		{"key-set miss", f("genres", true, "horror"), false},
		{"key-set label containing a colon", f("source", true, "gateway:nyaa.si"), true},

		// OR within one filter's values.
		{"or hits on the second value", f("fileType", true, "audio", "video"), true},
		{"or misses on all values", f("fileType", true, "audio", "image"), false},

		// Fail-closed on an absent field — mirrors record_matches, and is what
		// keeps a filtered query from returning unrelated records.
		{"absent field fails closed", f("imdbid", true, "tt0903747"), false},

		// A filter that constrains nothing must not empty the result.
		{"no values constrains nothing", f("fileType", true), true},
		{"blank value constrains nothing", f("fileType", true, "  "), true},
		{"no property constrains nothing", f("", true, "video"), true},

		// A FLAT field whose value happens to be "false" is still a value, and
		// an exact filter for it matches. Only the key-set arm treats "false"
		// as non-membership (pinned in the test below).
		{"flat field valued false still matches", f("retracted", true, "false"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordMatchesFilter(fields, &tc.filter); got != tc.want {
				t.Fatalf("recordMatchesFilter(%+v) = %v, want %v", tc.filter, got, tc.want)
			}
		})
	}
}

// `retracted = "false"` is a flat field, not a key-set member, so the flat arm
// legitimately matches the literal string "false". The key-set arm is what must
// treat it as non-membership — pinned separately so the two arms don't get
// conflated if either is refactored.
func TestFalseyKeySetMemberIsNotMembership(t *testing.T) {
	fields := map[string]string{"genres/Action": "false", "genres/Drama": "0"}
	for _, v := range []string{"Action", "Drama"} {
		filter := f("genres", true, v)
		if recordMatchesFilter(fields, &filter) {
			t.Fatalf("genres/%s with a falsey value must not assert membership", v)
		}
	}
}

func TestRecordMatchesFiltersIsAndAcrossEntries(t *testing.T) {
	fields := map[string]string{"fileType": "video", "season": "2"}

	all := []MetadataFilter{f("fileType", true, "video"), f("season", true, "2")}
	if !recordMatchesFilters(fields, all) {
		t.Fatal("a record satisfying every filter must match")
	}

	one := []MetadataFilter{f("fileType", true, "video"), f("season", true, "3")}
	if recordMatchesFilters(fields, one) {
		t.Fatal("filters are AND across entries — one miss must reject the record")
	}

	if !recordMatchesFilters(fields, nil) {
		t.Fatal("no filters must match everything")
	}
}

// The legacy Property/PropertyValue pair keeps its substring semantics: it is
// the shape existing callers already use, and tightening it silently would
// change results for them. New callers pass Filters and opt into Exact.
func TestNormalizedFiltersFoldsTheLegacyPair(t *testing.T) {
	got := normalizedFilters(MetadataSearchRequest{Property: "tmdbid", PropertyValue: "1405"})
	if len(got) != 1 {
		t.Fatalf("legacy pair must fold into exactly one filter, got %d", len(got))
	}
	if got[0].Property != "tmdbid" || len(got[0].Values) != 1 || got[0].Values[0] != "1405" {
		t.Fatalf("legacy pair folded incorrectly: %+v", got[0])
	}
	if got[0].Exact {
		t.Fatal("the legacy pair must stay substring (Exact=false) for compatibility")
	}

	// A half-specified pair is not a filter — it must not become one that
	// matches nothing and empties the response.
	if n := len(normalizedFilters(MetadataSearchRequest{Property: "tmdbid"})); n != 0 {
		t.Fatalf("a property with no value must not fold into a filter, got %d", n)
	}
	if n := len(normalizedFilters(MetadataSearchRequest{PropertyValue: "1405"})); n != 0 {
		t.Fatalf("a value with no property must not fold into a filter, got %d", n)
	}

	both := normalizedFilters(MetadataSearchRequest{
		Property:      "tmdbid",
		PropertyValue: "1405",
		Filters:       []MetadataFilter{f("fileType", true, "video")},
	})
	if len(both) != 2 {
		t.Fatalf("the legacy pair must be appended to explicit filters, got %d", len(both))
	}
}

// The acceptance bar for routing the anchored path through the index: a
// full-miss filtered sweep over a realistic corpus must stay in the same class
// as the free-text branch (measured 10 ms over 19 622 live records), not the
// legacy per-record Redis scan it replaces (measured 3.63 s over the same
// corpus). Full-miss is the worst case — the loop cannot short-circuit on the
// result limit, so every record is evaluated.
//
// Run: go test ./internal/api/ -bench BenchmarkFilterSweep -benchtime 10x
func BenchmarkFilterSweep(b *testing.B) {
	const records = 20000
	corpus := make([]map[string]string, records)
	for i := range corpus {
		corpus[i] = map[string]string{
			"title":                  "Some Release",
			"fileType":               "video",
			"videoType":              "tv",
			"tmdbid":                 "1405",
			"season":                 "2",
			"genres/Action":          "true",
			"genres/Drama":           "true",
			"source/gateway:nyaa.si": "true",
			"categories/TV/Anime":    "true",
			"indexer":                "nyaa.si",
			"fileName":               "some.release.s02e05.mkv",
			"description/eng":        "text",
			"poster":                 "bafy",
			"sizeByte":               "123456789",
			"contentKind":            "episode",
			"anchoredBy/tmdb:1405":   "true",
		}
	}
	// A miss on the identity field: the shape an id-anchored gateway query
	// takes, and the one that must not degrade.
	filters := []MetadataFilter{
		f("tmdbid", true, "999999"),
		f("fileType", true, "video"),
		f("genres", true, "action"),
	}
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		hits := 0
		for _, fields := range corpus {
			if recordMatchesFilters(fields, filters) {
				hits++
			}
		}
		if hits != 0 {
			b.Fatalf("expected a full miss, got %d hits", hits)
		}
	}
}
