package storage

import (
	"github.com/google/uuid"
)

// NewUUID returns a new UUIDv7 encoded as 26-char Crockford Base32 (ULID
// layout). Used as the root key for file metadata entries.
//
// Why this format over canonical 8-4-4-4-12 hex:
//   - 26 chars vs 36 — shorter Redis keys, less prefix space waste
//   - Time-sortable prefix — KEYS file:01JKR* returns entries minted in a
//     given time window, restoring some of the debug-ability lost vs the
//     previous midhash256:abc rooting
//   - Case-insensitive — no ambiguity when copying from logs
//   - Crockford alphabet skips I/L/O/U, so eyeballed inspection of a key
//     can't be confused for an ambient digit/letter typo
func NewUUID() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return encodeCrockford(u[:]), nil
}

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodeCrockford encodes a 16-byte value as 26 Crockford Base32 chars.
// 128 bits packs into 130 bits (26 × 5); the top 2 bits of the first char
// carry the high bits of byte 0, the remaining 3 are implicit zero. This
// matches the ULID encoding so the output is interchangeable with ULIDs in
// any tool that accepts them.
func encodeCrockford(b []byte) string {
	out := make([]byte, 26)
	out[0] = crockfordAlphabet[(b[0]&0xE0)>>5]
	out[1] = crockfordAlphabet[b[0]&0x1F]
	out[2] = crockfordAlphabet[(b[1]&0xF8)>>3]
	out[3] = crockfordAlphabet[((b[1]&0x07)<<2)|((b[2]&0xC0)>>6)]
	out[4] = crockfordAlphabet[(b[2]&0x3E)>>1]
	out[5] = crockfordAlphabet[((b[2]&0x01)<<4)|((b[3]&0xF0)>>4)]
	out[6] = crockfordAlphabet[((b[3]&0x0F)<<1)|((b[4]&0x80)>>7)]
	out[7] = crockfordAlphabet[(b[4]&0x7C)>>2]
	out[8] = crockfordAlphabet[((b[4]&0x03)<<3)|((b[5]&0xE0)>>5)]
	out[9] = crockfordAlphabet[b[5]&0x1F]
	out[10] = crockfordAlphabet[(b[6]&0xF8)>>3]
	out[11] = crockfordAlphabet[((b[6]&0x07)<<2)|((b[7]&0xC0)>>6)]
	out[12] = crockfordAlphabet[(b[7]&0x3E)>>1]
	out[13] = crockfordAlphabet[((b[7]&0x01)<<4)|((b[8]&0xF0)>>4)]
	out[14] = crockfordAlphabet[((b[8]&0x0F)<<1)|((b[9]&0x80)>>7)]
	out[15] = crockfordAlphabet[(b[9]&0x7C)>>2]
	out[16] = crockfordAlphabet[((b[9]&0x03)<<3)|((b[10]&0xE0)>>5)]
	out[17] = crockfordAlphabet[b[10]&0x1F]
	out[18] = crockfordAlphabet[(b[11]&0xF8)>>3]
	out[19] = crockfordAlphabet[((b[11]&0x07)<<2)|((b[12]&0xC0)>>6)]
	out[20] = crockfordAlphabet[(b[12]&0x3E)>>1]
	out[21] = crockfordAlphabet[((b[12]&0x01)<<4)|((b[13]&0xF0)>>4)]
	out[22] = crockfordAlphabet[((b[13]&0x0F)<<1)|((b[14]&0x80)>>7)]
	out[23] = crockfordAlphabet[(b[14]&0x7C)>>2]
	out[24] = crockfordAlphabet[((b[14]&0x03)<<3)|((b[15]&0xE0)>>5)]
	out[25] = crockfordAlphabet[b[15]&0x1F]
	return string(out)
}
