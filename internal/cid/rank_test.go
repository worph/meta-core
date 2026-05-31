package cid

import (
	"encoding/base32"
	"encoding/binary"
	"strings"
	"testing"
)

// buildCID assembles a multibase-base32 CIDv1 from a content codec, a
// multihash code, and a digest length (digest bytes are zero — only the
// header is under test). Mirrors the encoder in watcher/midhash.go and the
// TS CID.createV1(code, create(code, digest)) path.
func buildCID(codec, mhCode uint64, digestLen int) string {
	var b []byte
	b = binary.AppendUvarint(b, 1) // CIDv1
	b = binary.AppendUvarint(b, codec)
	b = binary.AppendUvarint(b, mhCode)
	b = binary.AppendUvarint(b, uint64(digestLen))
	b = append(b, make([]byte, digestLen)...)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return "b" + strings.ToLower(enc)
}

// Real CIDs produced by @metazla/meta-hash (from its main.test.ts). For
// digest CIDs meta-hash sets codec == multihash code == algorithm code.
const (
	cidCrc32   = "bagzafmqcathsbnrk"
	cidMd5     = "bahkqdvibcatpcfl7ine2n625eb5ml5mzkfaa"
	cidSha1    = "baeircff3eqvxxaqqot2lt242stkdtk3gq4c2vra"
	cidSha256  = "baejbeibku6ua2l6r5olnggx4pto2sii2s5jeeawgh4cbkhlbmp52gudbua"
	cidSha3256 = "baelbmidner7x3dd7t2kldh6wiq4w6lsye7qrwo6uoqot2t52zkhqk56fnq"
	cidSha3384 = "baekrkmeglipkjf33exqexdvijl4twipruay4wsbvt44akce43lq5rgccxgr7v6dwksmj3sxedakevc53g6ga"
)

func TestDecode_Fixtures(t *testing.T) {
	cases := []struct {
		name     string
		cid      string
		wantMh   uint64
	}{
		{"crc32", cidCrc32, CodeCrc32},
		{"md5", cidMd5, CodeMd5},
		{"sha1", cidSha1, CodeSha1},
		{"sha2-256", cidSha256, CodeSha256},
		{"sha3-256", cidSha3256, CodeSha3_256},
		{"sha3-384", cidSha3384, CodeSha3_384},
	}
	for _, tc := range cases {
		codec, mh, err := Decode(tc.cid)
		if err != nil {
			t.Errorf("Decode(%s %q) error: %v", tc.name, tc.cid, err)
			continue
		}
		// meta-hash digest CIDs use codec == multihash code.
		if codec != tc.wantMh || mh != tc.wantMh {
			t.Errorf("Decode(%s) = codec 0x%x mh 0x%x, want both 0x%x", tc.name, codec, mh, tc.wantMh)
		}
	}
}

func TestDecode_Rejects(t *testing.T) {
	for _, in := range []string{"", "Qmabc", "x", "bzzzz!!"} {
		if _, _, err := Decode(in); err == nil {
			t.Errorf("Decode(%q) expected error, got nil", in)
		}
	}
}

func TestRank_Tiers(t *testing.T) {
	ipfsDagPB := buildCID(CodecDagPB, CodeSha256, 32)        // chunked IPFS file
	rawSha256 := buildCID(CodecRaw, CodeSha256, 32)          // fullhash-style sha2-256
	btihV2meta := buildCID(CodeBtihV2, CodeBtihV2, 32)       // meta-hash btih-v2 (codec==mh)
	btihV2raw := buildCID(CodecRaw, CodeBtihV2, 32)          // fullhash btih-v2 (raw codec)
	btV1File := buildCID(CodeBtihV1File, CodeBtihV1File, 20) // gateway per-file btih v1
	midhashRaw := buildCID(CodecRaw, CodeMidhash256, 32)     // fullhash midhash (raw codec)

	cases := []struct {
		name string
		cid  string
		want int
	}{
		{"ipfs-dagpb", ipfsDagPB, 40},
		{"sha2-256(fixture, codec==mh)", cidSha256, 30},
		{"sha2-256(raw codec)", rawSha256, 30},
		{"sha3-256", cidSha3256, 30},
		{"sha3-384", cidSha3384, 30},
		{"btih-v2(codec==mh)", btihV2meta, 20},
		{"btih-v2(raw codec)", btihV2raw, 20},
		{"btih-v1-file", btV1File, 20},
		{"midhash256(raw codec)", midhashRaw, 10},
		{"sha1", cidSha1, 0},
		{"md5", cidMd5, 0},
		{"crc32", cidCrc32, 0},
		{"garbage", "not-a-cid", 0},
	}
	for _, tc := range cases {
		if got := Rank(tc.cid); got != tc.want {
			t.Errorf("Rank(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRank_StrictOrdering(t *testing.T) {
	ipfs := Rank(buildCID(CodecDagPB, CodeSha256, 32))
	sha := Rank(cidSha256)
	btih := Rank(buildCID(CodeBtihV2, CodeBtihV2, 32))
	mid := Rank(buildCID(CodeMidhash256, CodeMidhash256, 32))
	unknown := Rank(cidMd5)
	if !(ipfs > sha && sha > btih && btih > mid && mid > unknown) {
		t.Fatalf("rank ordering broken: ipfs=%d sha=%d btih=%d mid=%d unknown=%d",
			ipfs, sha, btih, mid, unknown)
	}
}

func TestBetter(t *testing.T) {
	dagpb := buildCID(CodecDagPB, CodeSha256, 32)
	// dag-pb (IPFS) beats a sha2-256 digest, regardless of arg order.
	if got := Better(dagpb, cidSha256); got != dagpb {
		t.Errorf("Better(dagpb, sha256) = %q, want dagpb", got)
	}
	if got := Better(cidSha256, dagpb); got != dagpb {
		t.Errorf("Better(sha256, dagpb) = %q, want dagpb (order shouldn't matter)", got)
	}
	// Tie → lexicographically smaller wins, deterministic.
	a, b := "bafyaaa", "bafyaab"
	if Better(a, b) != a || Better(b, a) != a {
		t.Errorf("tie-break not deterministic for %q vs %q", a, b)
	}
}
