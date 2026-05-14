package schema

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cidRegex matches the CID shapes produced by @metazla/meta-hash:
//   - CIDv1 multibase-base32: every algorithm (crc32, md5, sha1, sha2-256,
//     sha3-256, sha3-384, midhash256, btih-v2, …) is serialized via
//     CID.createV1(code, digest).toString(), which always yields a lowercase
//     base32 string starting with "ba" (the "b" is the multibase tag, the
//     "a" is the CIDv1 version byte under base32-encoding-of-0x01).
//   - CIDv0 base58btc: "Qm" + 44 chars, kept for compatibility with other
//     services that may still emit v0.
//
// Minimum CIDv1 length 16 chars matches the shortest real output we observed
// (crc32: "bafk3eaqerjhiwhy").
var cidRegex = regexp.MustCompile(`^(ba[a-z2-7]{14,}|Qm[1-9A-HJ-NP-Za-km-z]{44})$`)

// ClassifyValue returns the (primitive, hint) for a Redis value.
// exists=false (key missing) and JSON null literal both map to PrimUndefined.
// Empty string is a valid string.
func ClassifyValue(value string, exists bool) (Primitive, Hint) {
	if !exists {
		return PrimUndefined, ""
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "null" {
		return PrimUndefined, ""
	}
	if trimmed == "true" || trimmed == "false" {
		return PrimBool, ""
	}
	if _, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		if isTimestampInt(trimmed) {
			return PrimInt, HintTimestamp
		}
		return PrimInt, ""
	}
	if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return PrimFloat, ""
	}
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '{':
			var v map[string]any
			if json.Unmarshal([]byte(trimmed), &v) == nil {
				return PrimJSONObj, ""
			}
		case '[':
			var v []any
			if json.Unmarshal([]byte(trimmed), &v) == nil {
				return PrimJSONArr, ""
			}
		}
	}
	if cidRegex.MatchString(value) {
		return PrimString, HintCID
	}
	if isTimestampString(value) {
		return PrimString, HintTimestamp
	}
	return PrimString, ""
}

// isTimestampInt accepts unix-seconds in [2001-09, 2100] or unix-millis in the
// same range. Anything else is treated as a plain int.
func isTimestampInt(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return false
	}
	if n >= 1_000_000_000 && n <= 4_102_444_800 {
		return true
	}
	if n >= 1_000_000_000_000 && n <= 4_102_444_800_000 {
		return true
	}
	return false
}

func isTimestampString(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return true
	}
	return false
}

// breakdownLabel returns the most specific label for a classified value.
// Hints take precedence over primitives.
func breakdownLabel(prim Primitive, hint Hint) string {
	if hint != "" {
		return string(hint)
	}
	return string(prim)
}
