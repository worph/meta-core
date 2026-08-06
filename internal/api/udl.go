package api

// User Data Layer (UDL) HTTP surface.
//
// Backs meta-watch's per-user store (My List, Continue Watching, ratings,
// profile) now that the authoritative store lives in meta-core instead of
// meta-watch's local op-log. Records arrive already signed (and, for
// private-tier keys, already encrypted) as opaque base64 CBOR blobs; meta-core
// version-gates and indexes them but never inspects the crypto. See
// internal/storage/udl.go for the Redis model.

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
)

// UDLPutRequest is the body of PUT /api/udl/record.
type UDLPutRequest struct {
	Uid     string           `json:"uid"`
	Cid     string           `json:"cid"`
	Key     string           `json:"key"`
	Version int64            `json:"version"`
	Ts      int64            `json:"ts"`
	Record  string           `json:"record"` // base64 CBOR, opaque + signed
	Value   *json.RawMessage `json:"value,omitempty"`
	// Tombstone marks this write as a deletion of (uid, cid, key). The record
	// itself is opaque here — meta-core never decodes the CBOR, and a private-
	// tier value and a tombstone are equally "no plaintext value" — so the
	// writer has to say. Without it the cid is never removed from
	// udl:idx:user:<uid>:key:<key>, which then grows for the life of the
	// account and is returned in full on every My List / Continue Watching
	// read. Omitted by older writers, which keeps the previous behaviour.
	Tombstone bool `json:"tombstone,omitempty"`
}

// UDLPutResponse is the response of PUT /api/udl/record.
type UDLPutResponse struct {
	Accepted bool `json:"accepted"`
}

// UDLPutBatchRequest is the body of PUT /api/udl/records — many cells, one
// request. Each entry is exactly a UDLPutRequest, so a client can build the
// same signed record for either endpoint.
type UDLPutBatchRequest struct {
	Records []UDLPutRequest `json:"records"`
}

// UDLPutBatchResult is one cell's outcome. Accepted=false with an empty Error
// means the write was stale (the single-record endpoint's 409), which in a
// batch is an ordinary outcome rather than a failure.
type UDLPutBatchResult struct {
	Key      string `json:"key"`
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// UDLPutBatchResponse mirrors the request order index-for-index.
type UDLPutBatchResponse struct {
	Results []UDLPutBatchResult `json:"results"`
}

// maxUDLBatch bounds one batch. Each record is a Redis round trip while the
// request is held open; sized to match the sign batch on the other side of the
// same operation (see maxSignBatch).
const maxUDLBatch = 1000

// UDLRecordResponse is the response of GET /api/udl/record.
type UDLRecordResponse struct {
	Record  string `json:"record"`
	Version int64  `json:"version"`
	Ts      int64  `json:"ts"`
	Exists  bool   `json:"exists"`
	// Value is the plaintext JSON scalar stored beside the opaque record, and
	// is present only for PUBLIC-tier keys (like, rating, profile:name,
	// profile:avatar). json.RawMessage rather than string because the field is
	// generic over keys — a bool for `like`, a number for `rating`, a string
	// for a profile name (see aggregatePublicValues) — and because it is the
	// exact type UDLPutRequest.Value uses, so a value written through PUT reads
	// back byte-identical.
	//
	// Absent, never null, when there is no plaintext: private-tier cells,
	// tombstones and never-written cells must not read as an empty public
	// value. Readers key off presence, not `exists`.
	Value *json.RawMessage `json:"value,omitempty"`
}

// UDLAggregate mirrors meta-watch's Aggregate wire shape exactly (snake_case
// field names; `avg` is null for boolean keys like `like`), so the meta-watch
// client deserializes it into the same struct it serializes to the UI.
type UDLAggregate struct {
	Key        string   `json:"key"`
	Count      int      `json:"count"`
	Avg        *float64 `json:"avg"`
	Trues      int      `json:"trues"`
	SampleSize int      `json:"sample_size"`
}

// handleUDLRecordGet handles GET /api/udl/record?uid&cid&key
func (s *Server) handleUDLRecordGet(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	q := r.URL.Query()
	uid, cid, key := q.Get("uid"), q.Get("cid"), q.Get("key")
	if uid == "" || cid == "" || key == "" {
		writeError(w, http.StatusBadRequest, "uid, cid and key are required")
		return
	}

	cell, exists, err := s.storage.UDLGetCell(uid, cid, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := UDLRecordResponse{
		Record:  cell.Record,
		Version: cell.Version,
		Ts:      cell.Ts,
		Exists:  exists,
	}
	// json.Valid guards the encoder: a malformed `value` hash field (hand-edited
	// Redis, a future writer bug) would otherwise fail mid-encode and emit a
	// truncated body instead of a readable error.
	if exists && cell.HasValue && json.Valid([]byte(cell.Value)) {
		raw := json.RawMessage(cell.Value)
		resp.Value = &raw
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUDLRecordPut handles PUT /api/udl/record. Version-gated: a stale write
// (version <= stored) returns 409 stale_version so the client can re-read and
// retry with a higher version.
func (s *Server) handleUDLRecordPut(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	var req UDLPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Uid == "" || req.Cid == "" || req.Key == "" || req.Record == "" {
		writeError(w, http.StatusBadRequest, "uid, cid, key and record are required")
		return
	}

	publicValue := ""
	hasValue := false
	if req.Value != nil {
		publicValue = string(*req.Value)
		hasValue = true
	}

	accepted, err := s.storage.UDLUpsertIfNewer(req.Uid, req.Cid, req.Key, req.Version, req.Ts, req.Record, publicValue, hasValue, req.Tombstone)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepted {
		writeErrorSlug(w, http.StatusConflict, "stale_version", "a record with an equal or newer version already exists", true)
		return
	}
	writeJSON(w, http.StatusOK, UDLPutResponse{Accepted: true})
}

// handleUDLRecordsPut handles PUT /api/udl/records — the batch form of
// handleUDLRecordPut.
//
// Exists for the bulk verbs above this layer ("mark this whole series seen"),
// where one user action produces one cell per episode. Each cell still goes
// through the same version-gated Lua upsert, so the CRDT semantics are
// identical — this batches the transport only.
//
// **Not transactional, and deliberately not 409.** A per-record `accepted` flag
// replaces the single-record conflict status: in a batch, one stale cell is a
// normal outcome (another device already marked that episode) and must not fail
// the 299 cells beside it. Callers read the array, not the status code.
func (s *Server) handleUDLRecordsPut(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	var req UDLPutBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Records) == 0 {
		writeError(w, http.StatusBadRequest, "records is required")
		return
	}
	if len(req.Records) > maxUDLBatch {
		writeError(w, http.StatusBadRequest, "too many records in one batch")
		return
	}
	results := make([]UDLPutBatchResult, 0, len(req.Records))
	for _, rec := range req.Records {
		if rec.Uid == "" || rec.Cid == "" || rec.Key == "" || rec.Record == "" {
			results = append(results, UDLPutBatchResult{
				Key:   rec.Key,
				Error: "uid, cid, key and record are required",
			})
			continue
		}
		publicValue := ""
		hasValue := false
		if rec.Value != nil {
			publicValue = string(*rec.Value)
			hasValue = true
		}
		accepted, err := s.storage.UDLUpsertIfNewer(rec.Uid, rec.Cid, rec.Key, rec.Version, rec.Ts, rec.Record, publicValue, hasValue, rec.Tombstone)
		if err != nil {
			// A storage error is per-record here rather than fatal for the whole
			// batch: the cells already written stay written, and reporting which
			// ones failed lets the caller retry exactly those.
			results = append(results, UDLPutBatchResult{Key: rec.Key, Error: err.Error()})
			continue
		}
		results = append(results, UDLPutBatchResult{Key: rec.Key, Accepted: accepted})
	}
	writeJSON(w, http.StatusOK, UDLPutBatchResponse{Results: results})
}

// handleUDLUserKey handles GET /api/udl/user/{uid}/key/{key}
// (active_for_user_key — My List, Continue Watching).
func (s *Server) handleUDLUserKey(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	vars := mux.Vars(r)
	uid, key := vars["uid"], vars["key"]
	if uid == "" || key == "" {
		writeError(w, http.StatusBadRequest, "uid and key are required")
		return
	}
	entries, err := s.storage.UDLListUserKey(uid, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// handleUDLUserCid handles GET /api/udl/user/{uid}/cid/{cid}
// (list_for_user_cid).
func (s *Server) handleUDLUserCid(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	vars := mux.Vars(r)
	uid, cid := vars["uid"], vars["cid"]
	if uid == "" || cid == "" {
		writeError(w, http.StatusBadRequest, "uid and cid are required")
		return
	}
	entries, err := s.storage.UDLListUserCid(uid, cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"entries": entries})
}

// handleUDLCidUsers handles GET /api/udl/cid/{cid}/users (uids_for_cid).
func (s *Server) handleUDLCidUsers(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		writeError(w, http.StatusBadRequest, "cid is required")
		return
	}
	users, err := s.storage.UDLCidUsers(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})
}

// handleUDLAggregate handles GET /api/udl/cid/{cid}/aggregate?keys=like,rating.
// Computes per-key population stats from the stored public value fields
// (absorbs meta-watch's aggregate.rs). Reflects only records this core holds.
func (s *Server) handleUDLAggregate(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		writeError(w, http.StatusBadRequest, "cid is required")
		return
	}
	keysParam := r.URL.Query().Get("keys")
	if keysParam == "" {
		keysParam = "like,rating"
	}
	aggregates := make([]UDLAggregate, 0)
	for _, key := range strings.Split(keysParam, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values, err := s.storage.UDLPublicValues(cid, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		aggregates = append(aggregates, aggregatePublicValues(key, values))
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"aggregates": aggregates})
}

// aggregatePublicValues folds raw JSON scalar values into an UDLAggregate:
// counts booleans as trues and averages numeric values.
func aggregatePublicValues(key string, values []string) UDLAggregate {
	agg := UDLAggregate{Key: key, SampleSize: len(values), Count: len(values)}
	var sum float64
	var numeric int
	for _, raw := range values {
		var v interface{}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		switch t := v.(type) {
		case bool:
			if t {
				agg.Trues++
			}
		case float64:
			sum += t
			numeric++
		}
	}
	if numeric > 0 {
		avg := math.Round((sum/float64(numeric))*100) / 100
		agg.Avg = &avg
	}
	return agg
}
