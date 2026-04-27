package snapshot

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Flat keys use "/" as the separator. Property paths inside Redis are
// stored as e.g. "fileinfo/streamdetails/video/0/codec".
const flatSep = "/"

// numericKey matches a path segment that is a non-negative decimal integer.
// Used to detect array slots (".../video/0/codec").
var numericKey = regexp.MustCompile(`^\d+$`)

// Reconstruct rebuilds a nested JSON-shaped value from a flat property map.
// Keys are property paths (no leading slash, no file:{cid}/ prefix).
//
// Sibling keys whose path segments are all sequential integers starting at 0
// are emitted as JSON arrays. Other groups become JSON objects. Leaf values
// are coerced from string to int/float/bool/null when they parse cleanly so
// the exported JSON is human-readable; on import we round-trip back to strings.
func Reconstruct(flat map[string]string) interface{} {
	root := map[string]interface{}{}
	for path, value := range flat {
		parts := strings.Split(path, flatSep)
		setPath(root, parts, parseValue(value))
	}
	return convertNumericMapsToArrays(root)
}

// Flatten is the inverse of Reconstruct: walks a nested value and produces a
// flat property map whose values are all strings (Redis-ready).
//
// Arrays become numeric path segments ("genre/0", "genre/1"). Nil/missing
// values are stored as "" to preserve the field's presence — this matches
// what flattenMetadata in the TS implementation does.
func Flatten(value interface{}) map[string]string {
	out := map[string]string{}
	flattenInto(value, "", out)
	return out
}

func setPath(root map[string]interface{}, parts []string, leaf interface{}) {
	if len(parts) == 0 {
		return
	}
	cur := root
	for i, part := range parts {
		if i == len(parts)-1 {
			cur[part] = leaf
			return
		}
		next, ok := cur[part].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			cur[part] = next
		}
		cur = next
	}
}

func convertNumericMapsToArrays(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		// Recurse first so child arrays are detected before parent.
		for k, child := range t {
			t[k] = convertNumericMapsToArrays(child)
		}
		if !looksLikeArray(t) {
			return t
		}
		// Sort numerically and emit as []interface{}.
		idx := make([]int, 0, len(t))
		for k := range t {
			n, _ := strconv.Atoi(k)
			idx = append(idx, n)
		}
		sort.Ints(idx)
		arr := make([]interface{}, len(idx))
		for i, n := range idx {
			arr[i] = t[strconv.Itoa(n)]
		}
		return arr
	case []interface{}:
		for i := range t {
			t[i] = convertNumericMapsToArrays(t[i])
		}
		return t
	default:
		return v
	}
}

// looksLikeArray returns true when every key in m is a non-negative integer
// and the keys form a contiguous range starting at 0. Empty maps return false
// — we can't tell whether the original was {} or [], so prefer {}.
func looksLikeArray(m map[string]interface{}) bool {
	if len(m) == 0 {
		return false
	}
	seen := make(map[int]bool, len(m))
	for k := range m {
		if !numericKey.MatchString(k) {
			return false
		}
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 {
			return false
		}
		seen[n] = true
	}
	for i := 0; i < len(m); i++ {
		if !seen[i] {
			return false
		}
	}
	return true
}

func flattenInto(v interface{}, path string, out map[string]string) {
	switch t := v.(type) {
	case nil:
		out[path] = ""
	case map[string]interface{}:
		if len(t) == 0 && path != "" {
			// Empty objects don't have a Redis representation; skip.
			return
		}
		for k, child := range t {
			next := k
			if path != "" {
				next = path + flatSep + k
			}
			flattenInto(child, next, out)
		}
	case []interface{}:
		for i, child := range t {
			next := strconv.Itoa(i)
			if path != "" {
				next = path + flatSep + next
			}
			flattenInto(child, next, out)
		}
	case string:
		out[path] = t
	case bool:
		if t {
			out[path] = "true"
		} else {
			out[path] = "false"
		}
	case float64:
		// JSON numbers always decode to float64; emit ints without decimals.
		if t == float64(int64(t)) {
			out[path] = strconv.FormatInt(int64(t), 10)
		} else {
			out[path] = strconv.FormatFloat(t, 'f', -1, 64)
		}
	default:
		out[path] = fmt.Sprintf("%v", t)
	}
}

// parseValue mirrors the TS reconstruct logic: convert obvious primitives
// back to their typed form for nicer exported JSON, leave anything ambiguous
// as a string so we don't lose data (e.g. "5.1" channel layouts).
func parseValue(s string) interface{} {
	if s == "" {
		return nil
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	// Only treat as number when the string is a clean integer or decimal,
	// no leading zeros / trailing junk. Matches the TS regex.
	if isCleanNumber(s) {
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return n
		}
	}
	return s
}

var cleanNumber = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

func isCleanNumber(s string) bool { return cleanNumber.MatchString(s) }
