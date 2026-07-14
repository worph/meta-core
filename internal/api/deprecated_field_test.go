package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The per-algorithm CID fields (`cid_sha2-256`, `cid_midhash256`, …) and the
// stored `canonical_cid` scalar were removed (METADATA_KEYS.md §14.13). They
// must be rejected at the write boundary rather than stored.
//
// Why a hard 400 and not a silent rewrite: the reverse-index hook
// (maybeAddAliasFromFieldLocked) only registers an alias for a field named
// `cids/<cid>`. A `cid_*` field was therefore written verbatim and never
// indexed — the record silently became unresolvable by that CID. A loud
// failure is strictly better than a record that looks correct and cannot be
// found.
func TestRejectDeprecatedField(t *testing.T) {
	rejected := []string{
		"cid_midhash256",
		"cid_sha2-256",
		"cid_btih_v2",
		"cid_crc32",
		"midhash256",
		"canonical_cid",
	}
	for _, f := range rejected {
		if !rejectDeprecatedField(f) {
			t.Errorf("rejectDeprecatedField(%q) = false, want true (deprecated shape)", f)
		}
	}

	accepted := []string{
		"cids/bagacbabaec7v3fu2ygzh3e2sybg3fbzmisry2hbtpmck6vx3yftea6vzq35r4",
		"filePath",
		"title",
		"fileinfo/duration",
		"tmdb/poster",
		// Near-misses that must NOT be caught: the prefix is `cid_`, with an
		// underscore. A field merely starting with "cid" is fine.
		"cids",
		"cidrank",
	}
	for _, f := range accepted {
		if rejectDeprecatedField(f) {
			t.Errorf("rejectDeprecatedField(%q) = true, want false (legitimate field)", f)
		}
	}
}

func TestValidateFields_Rejects400WithSlug(t *testing.T) {
	rr := httptest.NewRecorder()
	blocked := validateFields(rr, map[string]string{
		"title":          "Inception",
		"cid_midhash256": "bagacbabaec7v3fu2ygzh3e2sybg3fbzmisry2hbtpmck6vx3yftea6vzq35r4",
	})

	if !blocked {
		t.Fatal("validateFields returned false for a payload carrying cid_midhash256; the write would have been stored unindexed")
	}
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if got := body["error"]; got != ErrDeprecatedField {
		t.Errorf("error slug = %v, want %q", got, ErrDeprecatedField)
	}
}

func TestValidateFields_AllowsCidsKeyset(t *testing.T) {
	rr := httptest.NewRecorder()
	blocked := validateFields(rr, map[string]string{
		"filePath": "/watch/Inception.mkv",
		"cids/bagacbabaec7v3fu2ygzh3e2sybg3fbzmisry2hbtpmck6vx3yftea6vzq35r4": "true",
		"cids/baejbeibku6ua2l6r5olnggx4pto2sii2s5jeeawgh4cbkhlbmp52gudbua":     "true",
	})
	if blocked {
		t.Fatalf("validateFields rejected a legitimate cids/ key-set write: %s", rr.Body.String())
	}
}
