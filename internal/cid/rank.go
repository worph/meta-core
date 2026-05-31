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
//   - midhash256 is the floor among real options: internally meaningful,
//     externally invisible.
//   - md5 / crc32 / plain sha1 / pieces-root / unknown rank 0.
func Rank(cidStr string) int {
	codec, mh, err := Decode(cidStr)
	if err != nil {
		return 0
	}
	if codec == CodecDagPB {
		return 40
	}
	// BitTorrent: distinct CID codecs (per-file v1/v2) or the btih-v2
	// multihash code, in either encoder convention.
	if codec == CodeBtihV1File || codec == CodeBtihV2File || codec == CodeBtihV2 ||
		mh == CodeBtihV1File || mh == CodeBtihV2File || mh == CodeBtihV2 {
		return 20
	}
	switch mh {
	case CodeSha256, CodeSha3_256, CodeSha3_384:
		return 30
	case CodeMidhash256:
		return 10
	default:
		// md5, crc32, plain sha1, pieces-root, unknown.
		return 0
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
