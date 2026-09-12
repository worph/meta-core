package storage

import (
	"context"
	"encoding/base32"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

// Pinned from meta-read `src/card_cid.rs` — literals, not round-trips.
const (
	literatureAnchorCardCID = "bagdsaaaua5qw42lmnfzxi3lbnztwcorrga2tonzy" // ("anilist", "manga:105778")
	literatureSiteCardCID   = "bagdsaabybrwwc3thmfsgk6bon5zgol3nmfxgoyjpmiygenzsgftgmlldgm4dqljugq4dmllbmeygmlldgjrdaytcgmzdcnjrgi"
	// A non-card CID a card may also carry.
	literatureContentCID = "bagacbabaec7v3fu2ygzh3e2sybg3fbzmisry2hbtpmck6vx3yftea6vzq35r4"
)

// mintCardCID encodes a card locator the way meta-feeder-sdk
// `compute_card_cid` does. Asserted against the pinned literals before use.
func mintCardCID(source, id string) string {
	digest := binary.AppendUvarint(nil, uint64(len(source)))
	digest = append(digest, source...)
	digest = append(digest, id...)
	wire := []byte{0x01}
	wire = binary.AppendUvarint(wire, 0x1007)
	wire = append(wire, 0x00)
	wire = binary.AppendUvarint(wire, uint64(len(digest)))
	wire = append(wire, digest...)
	return "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(wire))
}

func TestIsSiteFormCardLocator(t *testing.T) {
	if got := mintCardCID("anilist", "manga:105778"); got != literatureAnchorCardCID {
		t.Fatalf("test encoder drifted: %s != %s", got, literatureAnchorCardCID)
	}
	if got := mintCardCID("mangadex.org", "/manga/b0b721ff-c388-4486-aa0f-c2b0bb321512"); got != literatureSiteCardCID {
		t.Fatalf("test encoder drifted: %s != %s", got, literatureSiteCardCID)
	}

	for _, tc := range []struct {
		name string
		cid  string
		want bool
	}{
		{"anilist anchor (golden)", literatureAnchorCardCID, false},
		{"mangadex site (golden)", literatureSiteCardCID, true},
		{"openlibrary anchor", mintCardCID("openlibrary", "OL45804W"), false},
		{"another site", mintCardCID("weebcentral.com", "/series/01J76XY/chainsaw-man"), true},
		// Not an anchor source, but not host-shaped either: meta-read reads it
		// as an id-bag key, so it must not be treated as a site.
		{"dotless unknown source", mintCardCID("suwayomi", "42"), false},
		{"content CID", literatureContentCID, false},
		{"title key", "title:chainsaw man", false},
	} {
		if got := isSiteFormCardLocator(tc.cid); got != tc.want {
			t.Errorf("%s: isSiteFormCardLocator = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// seedLiteratureCorpus writes one record per rule the sweep has to honour.
func seedLiteratureCorpus(t *testing.T, c *Client) {
	t.Helper()

	// A site card carrying every echo: all of it goes.
	seedRecord(t, c, "site-card", map[string]string{
		"domain":                        "literature",
		"fileType":                      "card",
		"title":                         "Chainsaw Man",
		"cids/" + literatureSiteCardCID: "true",
		"anilistid":                     "105778",
		"workcid":                       literatureSiteCardCID,
		"chapterNumber":                 "12",
		"volumeNumber":                  "2",
		"chapterStart":                  "1",
		"chapterEnd":                    "20",
	})
	// An anchor card: its anilistid is its identity and stays; the position
	// echo and the workcid still go.
	seedRecord(t, c, "anchor-card", map[string]string{
		"domain":                          "literature",
		"fileType":                        "card",
		"title":                           "Chainsaw Man",
		"cids/" + literatureAnchorCardCID: "true",
		"anilistid":                       "105778",
		"workcid":                         literatureAnchorCardCID,
		"chapterNumber":                   "3",
	})
	// A site card that is already clean: nothing planned.
	seedRecord(t, c, "clean-site-card", map[string]string{
		"domain":                        "literature",
		"fileType":                      "card",
		"cids/" + literatureSiteCardCID: "true",
	})
	// Both locator forms: which one the id belongs to is unknowable, so it
	// stays.
	seedRecord(t, c, "merged-card", map[string]string{
		"domain":                          "literature",
		"fileType":                        "card",
		"cids/" + literatureSiteCardCID:   "true",
		"cids/" + literatureAnchorCardCID: "true",
		"anilistid":                       "105778",
	})
	// No card locator at all: not provably a site card.
	seedRecord(t, c, "unlocated-card", map[string]string{
		"domain":                       "literature",
		"fileType":                     "card",
		"cids/" + literatureContentCID: "true",
		"anilistid":                    "105778",
	})
	// A stored empty value is still an echo.
	seedRecord(t, c, "blank-echo-card", map[string]string{
		"domain":       "literature",
		"fileType":     "card",
		"volumeNumber": "",
	})
	// A chapter is never swept, whatever it carries.
	seedRecord(t, c, "chapter", map[string]string{
		"domain":                       "literature",
		"fileType":                     "archive",
		"cids/" + literatureContentCID: "true",
		"anilistid":                    "105778",
		"workcid":                      literatureSiteCardCID,
		"chapterNumber":                "12",
	})
	// Not literature: out of scope even though it is a card.
	seedRecord(t, c, "screen-card", map[string]string{
		"domain":        "screen",
		"fileType":      "card",
		"workcid":       literatureSiteCardCID,
		"chapterNumber": "1",
	})
}

func TestPlanLiteratureEchoes_ScopesToLiteratureCards(t *testing.T) {
	c, _ := newTestClient(t, "")
	seedLiteratureCorpus(t, c)

	plans, err := c.planLiteratureEchoes(context.Background())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := map[string][]string{}
	for _, p := range plans {
		got[p.hashID] = p.fields
	}
	want := map[string][]string{
		"site-card":       {"anilistid", "workcid", "chapterNumber", "volumeNumber", "chapterStart", "chapterEnd"},
		"anchor-card":     {"workcid", "chapterNumber"},
		"blank-echo-card": {"volumeNumber"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan =\n  %v\nwant\n  %v", got, want)
	}
}

func TestSweepLiteratureEchoes_DryRunThenApply(t *testing.T) {
	c, s := newTestClient(t, "mm:")
	seedLiteratureCorpus(t, c)

	wantByKey := map[string]int{
		"anilistid":     1,
		"workcid":       2,
		"chapterNumber": 2,
		"volumeNumber":  2,
		"chapterStart":  1,
		"chapterEnd":    1,
	}

	// Dry run: counts, and nothing written.
	dry, err := c.SweepLiteratureEchoes(context.Background(), false)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Planned != 3 || dry.Fixed != 0 {
		t.Errorf("dry run planned/fixed = %d/%d, want 3/0", dry.Planned, dry.Fixed)
	}
	if !reflect.DeepEqual(dry.ByKey, wantByKey) {
		t.Errorf("dry run byKey = %v, want %v", dry.ByKey, wantByKey)
	}
	if m, _ := c.GetMetadataFlat("site-card"); m["anilistid"] != "105778" {
		t.Fatalf("dry run deleted anilistid: %v", m)
	}

	applied, err := c.SweepLiteratureEchoes(context.Background(), true)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied.Planned != 3 || applied.Fixed != 3 {
		t.Errorf("apply planned/fixed = %d/%d, want 3/3", applied.Planned, applied.Fixed)
	}
	if !reflect.DeepEqual(applied.ByKey, wantByKey) {
		t.Errorf("apply byKey = %v, want %v", applied.ByKey, wantByKey)
	}

	site, err := c.GetMetadataFlat("site-card")
	if err != nil {
		t.Fatalf("read site-card: %v", err)
	}
	for _, k := range []string{"anilistid", "workcid", "chapterNumber", "volumeNumber", "chapterStart", "chapterEnd"} {
		if _, ok := site[k]; ok {
			t.Errorf("site-card still carries %s", k)
		}
		// The flat key and its field-name index entry must both be gone, or the
		// next read would MGET a ghost field.
		if s.Exists("mm:file:site-card/" + k) {
			t.Errorf("site-card flat key %s survived", k)
		}
		if member, _ := s.IsMember("mm:file:site-card/"+FieldsField, k); member {
			t.Errorf("site-card field index still lists %s", k)
		}
	}
	if site["title"] != "Chainsaw Man" || site["cids/"+literatureSiteCardCID] != "true" {
		t.Errorf("site-card lost fields it should keep: %v", site)
	}

	anchor, _ := c.GetMetadataFlat("anchor-card")
	if anchor["anilistid"] != "105778" {
		t.Errorf("anchor-card anilistid = %q, want kept", anchor["anilistid"])
	}
	if _, ok := anchor["workcid"]; ok {
		t.Errorf("anchor-card still carries workcid")
	}

	chapter, _ := c.GetMetadataFlat("chapter")
	if chapter["anilistid"] != "105778" || chapter["workcid"] != literatureSiteCardCID || chapter["chapterNumber"] != "12" {
		t.Errorf("chapter was touched: %v", chapter)
	}
	screen, _ := c.GetMetadataFlat("screen-card")
	if screen["workcid"] != literatureSiteCardCID {
		t.Errorf("screen-card was touched: %v", screen)
	}
	merged, _ := c.GetMetadataFlat("merged-card")
	if merged["anilistid"] != "105778" {
		t.Errorf("merged-card anilistid = %q, want kept", merged["anilistid"])
	}

	// Idempotent: the endpoint is called by hand, and a second call must find
	// nothing — with byKey still an empty map, not nil.
	again, err := c.SweepLiteratureEchoes(context.Background(), true)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.Planned != 0 || again.Fixed != 0 || again.ByKey == nil || len(again.ByKey) != 0 {
		t.Errorf("second pass = %+v, want nothing planned and an empty byKey", again)
	}
}
