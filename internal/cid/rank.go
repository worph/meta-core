// Package cid centralizes CID-format helpers: decoding a multibase-base32
// CIDv1 into its multicodec parts, ranking by utility for external
// presentation, and picking which CID is the canonical externally-visible
// identifier of a file.
//
// Sibling CIDs are stored as a bare-CID key-set (`cids/<cid> = "true"`); a
// CID is self-describing — its algorithm is recoverable from the CIDv1
// codec / multihash code, so there is no `<algorithm>:` prefix and no
// per-algorithm field name. Ranking and selection parse the multicodec
// rather than a string prefix.
package cid

import (
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
)

// Multicodec / multihash codes used for ranking. Source of truth for the
// digest codes is meta-hash/src/lib/hash-compute/MultiHashData.ts and
// METADATA_KEYS.md §2.
//
// The tier every code maps to is pinned by the golden fixture
// `cid-rank-vectors.json` (vendored next to this package). This is one of
// seven implementations of the same ladder across Go, Rust and TypeScript;
// the fixture is the only thing keeping them in agreement. If you add a code
// here, add a vector — an unranked code silently falls to tier 0, which is
// exactly how the nzb-release/url locators came to be ranked 20 in meta-share
// and 0 here and in meta-search.
const (
	CodecRaw   = 0x55   // raw — IPFS content-addressed block
	CodecDagPB = 0x70   // dag-pb — IPFS content-addressed block
	CodeSha1   = 0x11   // multihash sha1
	CodeSha256 = 0x12   // multihash sha2-256
	CodeSha3_384 = 0x15 // multihash sha3-384
	CodeSha3_256 = 0x16 // multihash sha3-256
	CodeMd5    = 0xd5   // multihash md5
	CodeCrc32  = 0x0132 // multihash crc32
	CodeMidhash256  = 0x1000 // custom — SHA-256(size‖middle 1MB)
	CodeBtPiecesRoot = 0xb702 // bittorrent v2 pieces root
	CodeBtihV2      = 0x10B7  // custom — BitTorrent v2 info hash (BEP 52)
	CodeBtihV1File  = 0x1001  // custom — per-file BitTorrent v1 CID
	CodeBtihV2File  = 0x1002  // custom — per-file BitTorrent v2 CID

	// Opaque locators. These are NOT digests: the payload rides in an identity
	// multihash (0x00), so the code identifying them sits in the *content
	// codec* slot, never the multihash slot. Rank must match on the codec for
	// these — matching on mh would see 0x00 and rank them 0.
	CodeNzbRelease = 0x1005 // custom — self-describing Newznab release locator
	CodeURL        = 0x1006 // custom — identity-multihash CID wrapping an http(s) URL
)

// Rank tiers, named so the ladder reads as a ladder.
const (
	RankIPFS    = 40 // dag-pb — broadest network reach
	RankDigest  = 30 // sha2-256 / sha3-* — real content digests
	RankBtih    = 20 // BitTorrent infohashes — swarm interop
	RankMidhash = 10 // the record address — internally meaningful, externally invisible
	RankLocator = 5  // opaque locators — never outrank a real digest
	RankUnknown = 0  // weak/unknown digests, unparseable input
)

// Decode parses a multibase-base32 CIDv1 string ("b…") into its content
// codec and multihash function code. It is the inverse of the encoder in
// internal/watcher/midhash.go. Returns an error for anything that isn't a
// CIDv1 in base32lower (e.g. a CIDv0 "Qm…" or a non-CID string).
func Decode(cidStr string) (codec uint64, mhCode uint64, err error) {
	if len(cidStr) < 2 || cidStr[0] != 'b' {
		return 0, 0, fmt.Errorf("cid %q: not a multibase base32 CIDv1", cidStr)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(cidStr[1:]))
	if err != nil {
		return 0, 0, fmt.Errorf("cid %q: base32 decode: %w", cidStr, err)
	}
	version, n := binary.Uvarint(raw)
	if n <= 0 {
		return 0, 0, fmt.Errorf("cid %q: bad version varint", cidStr)
	}
	raw = raw[n:]
	if version != 1 {
		return 0, 0, fmt.Errorf("cid %q: unsupported CID version %d", cidStr, version)
	}
	codec, n = binary.Uvarint(raw)
	if n <= 0 {
		return 0, 0, fmt.Errorf("cid %q: bad codec varint", cidStr)
	}
	raw = raw[n:]
	mhCode, n = binary.Uvarint(raw)
	if n <= 0 {
		return 0, 0, fmt.Errorf("cid %q: bad multihash-code varint", cidStr)
	}
	return codec, mhCode, nil
}

// Rank returns a score for a bare CID. Higher = more preferred as the
// public-facing "canonical" identifier for a file. Unknown / undecodable
// CIDs rank 0 so a new digest type can't accidentally outrank an
// established one until it's classified here.
//
// We rank by the **multihash code** (the algorithm), with the custom
// BitTorrent CID codecs checked alongside it. This is deliberate: MetaMesh's
// two CID encoders disagree on the *content codec* — meta-hash/the watcher
// set codec == algorithm code, while the fullhash plugin and gateway wrap
// every digest with the raw codec (0x55) and put the algorithm in the
// multihash code. The multihash code is the algorithm in all of them, so it
// is the only reliable discriminator. (The BT info hash is given a distinct
// multihash code — btih-v2 0x10B7 — so it can't be confused with a plain
// sha2-256, see METADATA_KEYS.md §14.13.)
//
// Ordering rationale (utility-weighted, NOT cryptographic-strength):
//   - dag-pb (chunked IPFS file) wins: best retrieval over the IPFS network.
//   - sha2-256 / sha3 digests follow: standard content hashes (a raw IPFS
//     block of the file IS its sha2-256, so they share this tier).
//   - BitTorrent infohashes (btih v2, per-file v1/v2) outrank midhash256 for
//     swarm interop, not strength.
//   - midhash256 is the floor among real *digests*: internally meaningful,
//     externally invisible.
//   - opaque locators (nzb-release, url) sit BELOW midhash. They are not
//     content-derived — two URLs to the same bytes yield two different locator
//     CIDs — so a locator must never displace a real digest as the identity a
//     record is advertised under. Tier 5 means a locator wins the election only
//     when it is the record's sole CID, which is exactly the guarantee
//     METADATA_KEYS.md §2 states.
//   - md5 / crc32 / plain sha1 / pieces-root / unknown rank 0.
func Rank(cidStr string) int {
	codec, mh, err := Decode(cidStr)
	if err != nil {
		return RankUnknown
	}
	if codec == CodecDagPB {
		return RankIPFS
	}
	// BitTorrent: distinct CID codecs (per-file v1/v2) or the btih-v2
	// multihash code, in either encoder convention.
	if codec == CodeBtihV1File || codec == CodeBtihV2File || codec == CodeBtihV2 ||
		mh == CodeBtihV1File || mh == CodeBtihV2File || mh == CodeBtihV2 {
		return RankBtih
	}
	// Opaque locators. Codec-only: their multihash is identity (0x00), so
	// there is nothing to match on the mh side.
	if codec == CodeNzbRelease || codec == CodeURL {
		return RankLocator
	}
	switch mh {
	case CodeSha256, CodeSha3_256, CodeSha3_384:
		return RankDigest
	case CodeMidhash256:
		return RankMidhash
	default:
		// md5, crc32, plain sha1, pieces-root, unknown.
		return RankUnknown
	}
}

// Better picks the higher-ranked of two CIDs. On ties, returns the
// lexicographically smaller one — gives the reconciler a deterministic
// answer so the chosen canonical doesn't churn when peers add the same
// digest under a different ordering.
func Better(a, b string) string {
	ra, rb := Rank(a), Rank(b)
	if ra > rb {
		return a
	}
	if rb > ra {
		return b
	}
	if a <= b {
		return a
	}
	return b
}
