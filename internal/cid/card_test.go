package cid

import (
	"encoding/base32"
	"encoding/binary"
	"strings"
	"testing"
)

// Pinned from meta-read `src/card_cid.rs` / meta-feeder-sdk `compute_card_cid`
// — literals, not round-trips (see DecodeCard).
const (
	goldenAnilistCard  = "bagdsaaaua5qw42lmnfzxi3lbnztwcorrga2tonzy"
	goldenMangadexCard = "bagdsaabybrwwc3thmfsgk6bon5zgol3nmfxgoyjpmiygenzsgftgmlldgm4dqljugq4dmllbmeygmlldgjrdaytcgmzdcnjrgi"
)

// wireCID builds a CIDv1 from raw parts so the negative cases below can vary
// one field at a time.
func wireCID(codec, mh uint64, digest []byte) string {
	buf := []byte{0x01}
	buf = binary.AppendUvarint(buf, codec)
	buf = binary.AppendUvarint(buf, mh)
	buf = binary.AppendUvarint(buf, uint64(len(digest)))
	buf = append(buf, digest...)
	return "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf))
}

func cardDigest(source, id string) []byte {
	d := binary.AppendUvarint(nil, uint64(len(source)))
	d = append(d, source...)
	return append(d, id...)
}

func TestDecodeCard_GoldenVectors(t *testing.T) {
	for _, tc := range []struct {
		cid, source, id string
	}{
		{goldenAnilistCard, "anilist", "manga:105778"},
		{goldenMangadexCard, "mangadex.org", "/manga/b0b721ff-c388-4486-aa0f-c2b0bb321512"},
	} {
		source, id, err := DecodeCard(tc.cid)
		if err != nil {
			t.Fatalf("DecodeCard(%s): %v", tc.cid, err)
		}
		if source != tc.source || id != tc.id {
			t.Errorf("DecodeCard(%s) = (%q, %q), want (%q, %q)", tc.cid, source, id, tc.source, tc.id)
		}
		// The test encoder must agree with the pinned literal, or the negative
		// cases below are testing a different wire format.
		if got := wireCID(CodeCard, 0, cardDigest(tc.source, tc.id)); got != tc.cid {
			t.Errorf("wireCID(%q, %q) = %s, want %s", tc.source, tc.id, got, tc.cid)
		}
	}
}

func TestDecodeCard_RejectsEverythingElse(t *testing.T) {
	for name, s := range map[string]string{
		"url locator codec":      wireCID(CodeURL, 0, cardDigest("anilist", "1")),
		"non-identity multihash": wireCID(CodeCard, CodeSha256, cardDigest("anilist", "1")),
		"empty id":               wireCID(CodeCard, 0, cardDigest("anilist", "")),
		"source past digest":     wireCID(CodeCard, 0, []byte{0x20, 'a'}),
		"not a CID":              "title:berserk",
		"CIDv0":                  "QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG",
		"truncated":              goldenAnilistCard[:12],
		"empty":                  "",
	} {
		if source, id, err := DecodeCard(s); err == nil {
			t.Errorf("%s: DecodeCard(%q) = (%q, %q), want error", name, s, source, id)
		}
	}
}
