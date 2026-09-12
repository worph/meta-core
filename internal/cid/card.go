package cid

import (
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

// DecodeCard recovers the (source, id) pair a card locator (CodeCard, 0x1007)
// wraps. Wire form:
//
//	0x01 ‖ varint(0x1007) ‖ 0x00 (identity mh) ‖ varint(len(digest)) ‖ digest
//	digest = varint(len(source)) ‖ source ‖ id        (both UTF-8)
//
// Decode stops at the multihash code because ranking needs nothing past it;
// reading *which work* a card addresses needs the digest too, so this is the
// one place in Go that walks the whole thing.
//
// ⚠ Mirror of meta-read `card_cid::decode` and of meta-feeder-sdk
// `compute_card_cid`. The vectors in card_test.go are pinned literals copied
// from meta-read, not round-trips, so a drift on either side fails a test
// instead of silently reading a different work out of the same CID.
//
// Identity multihash only: a 0x1007 CID carrying any other multihash is not
// something anyone mints, and reading its digest as (source, id) would be
// reading hash bytes as text. Bytes past the declared digest length are
// ignored, as meta-read does.
func DecodeCard(cidStr string) (source, id string, err error) {
	if len(cidStr) < 2 || cidStr[0] != 'b' {
		return "", "", fmt.Errorf("cid %q: not a multibase base32 CIDv1", cidStr)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(cidStr[1:]))
	if err != nil {
		return "", "", fmt.Errorf("cid %q: base32 decode: %w", cidStr, err)
	}

	next := func(buf []byte, what string) (uint64, []byte, error) {
		v, n := binary.Uvarint(buf)
		if n <= 0 {
			return 0, nil, fmt.Errorf("cid %q: bad %s varint", cidStr, what)
		}
		return v, buf[n:], nil
	}

	version, raw, err := next(raw, "version")
	if err != nil {
		return "", "", err
	}
	if version != 1 {
		return "", "", fmt.Errorf("cid %q: unsupported CID version %d", cidStr, version)
	}
	codec, raw, err := next(raw, "codec")
	if err != nil {
		return "", "", err
	}
	if codec != CodeCard {
		return "", "", fmt.Errorf("cid %q: codec 0x%x is not a card locator", cidStr, codec)
	}
	mh, raw, err := next(raw, "multihash-code")
	if err != nil {
		return "", "", err
	}
	if mh != 0 {
		return "", "", fmt.Errorf("cid %q: card locator with non-identity multihash 0x%x", cidStr, mh)
	}
	digestLen, raw, err := next(raw, "digest-length")
	if err != nil {
		return "", "", err
	}
	if digestLen > uint64(len(raw)) {
		return "", "", fmt.Errorf("cid %q: digest truncated", cidStr)
	}
	digest := raw[:digestLen]

	sourceLen, rest, err := next(digest, "source-length")
	if err != nil {
		return "", "", err
	}
	if sourceLen > uint64(len(rest)) {
		return "", "", fmt.Errorf("cid %q: source truncated", cidStr)
	}
	source, id = string(rest[:sourceLen]), string(rest[sourceLen:])
	if source == "" || id == "" {
		return "", "", fmt.Errorf("cid %q: empty source or id", cidStr)
	}
	if !utf8.ValidString(source) || !utf8.ValidString(id) {
		return "", "", fmt.Errorf("cid %q: source or id is not UTF-8", cidStr)
	}
	return source, id, nil
}
