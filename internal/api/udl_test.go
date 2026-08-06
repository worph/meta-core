package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/metazla/meta-core/internal/storage"
)

// newUDLTestServer returns a Server wired to an in-memory Redis.
//
// The struct is built directly rather than through NewServer: NewServer boots
// the file watcher, mounts manager, watchers poller and WebDAV handler, none of
// which the UDL handlers touch — they use s.storage and nothing else.
func newUDLTestServer(t *testing.T) *Server {
	t.Helper()

	mr := miniredis.RunT(t)
	c := storage.NewClient("")
	if err := c.Connect("redis://" + mr.Addr()); err != nil {
		t.Fatalf("connect to miniredis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &Server{storage: c}
}

// getRecord drives handleUDLRecordGet and decodes the body into raw JSON
// fields, so a test can assert on the *encoding* of `value` (is it a quoted
// string? is the key there at all?) rather than on what a typed struct would
// have coerced it into.
func getRecord(t *testing.T, s *Server, uid, cid, key string) (int, map[string]json.RawMessage) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/udl/record?uid="+uid+"&cid="+cid+"&key="+key, nil)
	rr := httptest.NewRecorder()
	s.handleUDLRecordGet(rr, req)

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	return rr.Code, body
}

// The plaintext value of a PUBLIC-tier cell must come back over the wire, and
// come back *as it was written* — a JSON string stays a quoted string, a bool
// stays a bool.
//
// Both halves matter. Without the value at all, the meta-core dashboard can
// only ever show 44-character base58 uids: display names live in the User Data
// Layer as the public cell (uid, "self", "profile:name"), and the plaintext
// sits in Redis where the storage layer has always read it — the handler just
// used to drop it. And the type has to stay json.RawMessage: this endpoint is
// generic over keys, so a `*string` would mangle `like` (bool) and `rating`
// (number) while looking correct for names.
func TestUDLRecordGet_ReturnsPublicValue(t *testing.T) {
	s := newUDLTestServer(t)

	const uid = "zAlice"
	if ok, err := s.storage.UDLUpsertIfNewer(uid, "self", "profile:name", 1, 100, "cmVj", `"Ada Lovelace"`, true, false); err != nil || !ok {
		t.Fatalf("seed name: ok=%v err=%v", ok, err)
	}
	if ok, err := s.storage.UDLUpsertIfNewer(uid, "bagcsaaa", "like", 1, 100, "cmVj", `true`, true, false); err != nil || !ok {
		t.Fatalf("seed like: ok=%v err=%v", ok, err)
	}

	code, body := getRecord(t, s, uid, "self", "profile:name")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := string(body["value"]); got != `"Ada Lovelace"` {
		t.Fatalf("value = %s, want a quoted JSON string", got)
	}
	var name string
	if err := json.Unmarshal(body["value"], &name); err != nil || name != "Ada Lovelace" {
		t.Fatalf("value should unmarshal to the plain name, got %q err=%v", name, err)
	}

	// A boolean key survives unquoted — the assertion a *string field fails.
	if _, body = getRecord(t, s, uid, "bagcsaaa", "like"); string(body["value"]) != "true" {
		t.Fatalf("like value = %s, want the bare JSON literal true", body["value"])
	}
}

// A cell with no plaintext must omit `value` entirely rather than send null.
//
// That is the shape of every private-tier key (seek, watched, card, localpath —
// their real value rides encrypted inside the opaque record) and of every
// tombstone. A reader keys off presence: "there is no plaintext here" and "the
// plaintext is null" are different facts, and a name cleared by its owner
// leaves the cell existing with no value, which must not read as a name.
func TestUDLRecordGet_OmitsValueForPrivateCell(t *testing.T) {
	s := newUDLTestServer(t)

	const uid = "zBob"
	if ok, err := s.storage.UDLUpsertIfNewer(uid, "bagcsaaa", "seek", 1, 100, "ZW5jcnlwdGVk", "", false, false); err != nil || !ok {
		t.Fatalf("seed private cell: ok=%v err=%v", ok, err)
	}

	code, body := getRecord(t, s, uid, "bagcsaaa", "seek")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if _, ok := body["value"]; ok {
		t.Fatalf("private cell must omit `value`, got %s", body["value"])
	}
	if string(body["exists"]) != "true" {
		t.Fatalf("exists = %s, want true", body["exists"])
	}
}

// A cell that was never written answers 200 with exists:false and no value —
// not a 404, and not an empty-string value that would render as a blank name.
func TestUDLRecordGet_MissingCellHasNoValue(t *testing.T) {
	s := newUDLTestServer(t)

	code, body := getRecord(t, s, "zNobody", "self", "profile:name")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if string(body["exists"]) != "false" {
		t.Fatalf("exists = %s, want false", body["exists"])
	}
	if _, ok := body["value"]; ok {
		t.Fatalf("missing cell must omit `value`, got %s", body["value"])
	}
}

// putRecords drives handleUDLRecordsPut with a raw JSON body and decodes the
// per-record results.
func putRecords(t *testing.T, s *Server, body string) (int, UDLPutBatchResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPut, "/api/udl/records", strings.NewReader(body))
	rr := httptest.NewRecorder()
	s.handleUDLRecordsPut(rr, req)

	var out UDLPutBatchResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response %q: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, out
}

// **The load-bearing property of the batch endpoint: one stale cell does not
// fail the batch.**
//
// The single-record PUT answers a stale version with 409, which is right when
// the caller wrote one thing. In a batch it would be wrong: bulk verbs ("mark
// this whole series seen") routinely include episodes another device already
// marked, and 409-ing there would abandon every cell after it. So staleness is
// reported per record as accepted:false, with 200 on the request itself.
func TestUDLRecordsPut_StaleCellDoesNotFailTheBatch(t *testing.T) {
	s := newUDLTestServer(t)

	const uid = "zCarol"
	const cid = "bagdsaaaseries"
	// Seed s01e01 at version 4 — the batch below tries to write it at 2.
	if ok, err := s.storage.UDLUpsertIfNewer(uid, cid, "seen:s01e01", 4, 100, "b2xk", "", false, false); err != nil || !ok {
		t.Fatalf("seed: ok=%v err=%v", ok, err)
	}

	code, out := putRecords(t, s, `{"records":[
		{"uid":"`+uid+`","cid":"`+cid+`","key":"seen:s01e01","version":2,"ts":200,"record":"c3RhbGU"},
		{"uid":"`+uid+`","cid":"`+cid+`","key":"seen:s01e02","version":1,"ts":200,"record":"ZnJlc2g"}
	]}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a stale cell is an outcome, not an error", code)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %d, want 2 (one per record, in order)", len(out.Results))
	}
	if out.Results[0].Accepted || out.Results[0].Error != "" {
		t.Fatalf("stale cell = %+v, want accepted:false with no error", out.Results[0])
	}
	if !out.Results[1].Accepted {
		t.Fatalf("fresh cell = %+v, want accepted:true", out.Results[1])
	}

	// The fresh cell really landed, and the stale one did not overwrite.
	if _, body := getRecord(t, s, uid, cid, "seen:s01e02"); string(body["exists"]) != "true" {
		t.Fatalf("s01e02 was not written")
	}
	if _, body := getRecord(t, s, uid, cid, "seen:s01e01"); string(body["version"]) != "4" {
		t.Fatalf("s01e01 version = %s, want 4 (the stale write must not apply)", body["version"])
	}
}

// A malformed record is reported in its own slot rather than rejecting the
// request, for the same reason: the caller retries that cell, not the batch.
func TestUDLRecordsPut_ReportsBadRecordPerSlot(t *testing.T) {
	s := newUDLTestServer(t)

	code, out := putRecords(t, s, `{"records":[
		{"uid":"zDan","cid":"bagdsaaa","key":"","version":1,"ts":1,"record":"eA"},
		{"uid":"zDan","cid":"bagdsaaa","key":"seen:s01e01","version":1,"ts":1,"record":"eA"}
	]}`)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if out.Results[0].Error == "" {
		t.Fatalf("record with no key must report an error, got %+v", out.Results[0])
	}
	if !out.Results[1].Accepted {
		t.Fatalf("the valid record beside it must still land, got %+v", out.Results[1])
	}
}

// An empty batch is a client bug, not a no-op: it almost always means the
// caller built its record list wrong, and answering 200 with an empty array
// would hide that.
func TestUDLRecordsPut_RejectsEmptyBatch(t *testing.T) {
	s := newUDLTestServer(t)

	if code, _ := putRecords(t, s, `{"records":[]}`); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}
