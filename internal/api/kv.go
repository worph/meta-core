package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/metazla/meta-core/internal/storage"
)

// KVInfoResponse is the response for GET /api/kv/info
type KVInfoResponse struct {
	Prefix      string `json:"prefix"`
	FileCount   int    `json:"fileCount"`
	KeyCount    int    `json:"keyCount"`
	TotalSize   int64  `json:"totalSize"`
	MemoryUsage string `json:"memoryUsage"`
}

// KVKeysResponse is the response for GET /api/kv/keys
type KVKeysResponse struct {
	Keys    []string `json:"keys"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"hasMore"`
}

// KVKeyValueResponse is the response for GET /api/kv/key/{key}
type KVKeyValueResponse struct {
	Key   string            `json:"key"`
	Type  string            `json:"type"`
	Value map[string]string `json:"value"`
}

// handleKVInfo handles GET /api/kv/info
func (s *Server) handleKVInfo(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Get all hash IDs to count files
	hashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get memory usage
	memoryUsage, err := s.storage.GetMemoryInfo()
	if err != nil {
		memoryUsage = "N/A"
	}

	// Calculate total size from sizeByte metadata (if available)
	var totalSize int64
	for _, hashID := range hashIDs {
		sizeStr, err := s.storage.GetProperty(hashID, "sizeByte")
		if err == nil && sizeStr != "" {
			if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
				totalSize += size
			}
		}
	}

	response := KVInfoResponse{
		Prefix:      "file:",
		FileCount:   len(hashIDs),
		KeyCount:    len(hashIDs),
		TotalSize:   totalSize,
		MemoryUsage: memoryUsage,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleKVKeys handles GET /api/kv/keys
func (s *Server) handleKVKeys(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Parse query parameters
	cursorStr := r.URL.Query().Get("cursor")
	countStr := r.URL.Query().Get("count")

	cursor := 0
	if cursorStr != "" {
		if c, err := strconv.Atoi(cursorStr); err == nil {
			cursor = c
		}
	}

	count := 50
	if countStr != "" {
		if c, err := strconv.Atoi(countStr); err == nil && c > 0 {
			count = c
		}
	}

	// Get all hash IDs
	allHashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Simple offset-based pagination
	var keys []string
	end := cursor + count
	if end > len(allHashIDs) {
		end = len(allHashIDs)
	}

	if cursor < len(allHashIDs) {
		for _, id := range allHashIDs[cursor:end] {
			keys = append(keys, "file:"+id)
		}
	}

	nextCursor := cursor + len(keys)
	hasMore := nextCursor < len(allHashIDs)

	response := KVKeysResponse{
		Keys:    keys,
		Cursor:  strconv.Itoa(nextCursor),
		HasMore: hasMore,
	}

	if !hasMore {
		response.Cursor = "0"
	}

	writeJSON(w, http.StatusOK, response)
}

// handleKVGetKey handles GET /api/kv/key/{key}
func (s *Server) handleKVGetKey(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key := vars["key"]

	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	// URL decode the key (in case it contains encoded characters)
	decodedKey, err := url.PathUnescape(key)
	if err != nil {
		decodedKey = key
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Extract hash ID from key (format: file:{hashId})
	if !strings.HasPrefix(decodedKey, "file:") {
		writeError(w, http.StatusBadRequest, "invalid key format, expected: file:{hashId}")
		return
	}

	hashID := strings.TrimPrefix(decodedKey, "file:")

	// Get metadata for this hash ID
	metadata, err := s.storage.GetMetadataFlat(hashID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if metadata == nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}

	response := KVKeyValueResponse{
		Key:   decodedKey,
		Type:  "hash",
		Value: metadata,
	}

	writeJSON(w, http.StatusOK, response)
}

// KVTreeResponse is the response for GET /api/kv/tree
type KVTreeResponse struct {
	Prefix    string   `json:"prefix"`
	Delimiter string   `json:"delimiter"`
	Branches  []string `json:"branches"`
	Leaves    []string `json:"leaves"`
	Cursor    string   `json:"cursor"`
	HasMore   bool     `json:"hasMore"`
}

// KVSearchResponse is the response for GET /api/kv/search
type KVSearchResponse struct {
	Contains  string   `json:"contains"`
	Keys      []string `json:"keys"`
	Truncated bool     `json:"truncated"`
}

// KVFindResponse is the response for GET /api/kv/find
type KVFindResponse struct {
	Contains  string             `json:"contains"`
	Fields    []string           `json:"fields"`
	Matches   []storage.FindMatch `json:"matches"`
	Truncated bool               `json:"truncated"`
}

// KVValueResponse is the response for GET /api/kv/value
type KVValueResponse struct {
	Key    string `json:"key"`
	Type   string `json:"type"`
	Value  string `json:"value"`
	Exists bool   `json:"exists"`
}

// KVValuePutRequest is the body for PUT /api/kv/value
type KVValuePutRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KVValueDeleteResponse is the response for DELETE /api/kv/value
type KVValueDeleteResponse struct {
	Key     string `json:"key"`
	Deleted bool   `json:"deleted"`
}

// handleKVTree handles GET /api/kv/tree?prefix=&delimiter=/&cursor=0&max=500
//
// Lists the immediate children at prefix. When delimiter is set, keys with a
// further delimiter are collapsed into branches. Pass the returned cursor back
// to continue when hasMore is true.
func (s *Server) handleKVTree(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")
	cursor := q.Get("cursor")

	maxResults := 500
	if v := q.Get("max"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxResults = n
		}
	}

	leaves, branches, nextCursor, err := s.storage.ScanKeys(prefix, delimiter, cursor, maxResults)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, KVTreeResponse{
		Prefix:    prefix,
		Delimiter: delimiter,
		Branches:  branches,
		Leaves:    leaves,
		Cursor:    nextCursor,
		HasMore:   nextCursor != "0",
	})
}

// handleKVFind handles GET /api/kv/find?contains=&fields=a,b,c&limit=200
//
// Value-search: scans every key whose path ends with /<field> for each
// configured field, then returns those whose value contains the substring
// (case-insensitive). Used as the editor's "filter the view" action.
func (s *Server) handleKVFind(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	q := r.URL.Query()
	contains := q.Get("contains")
	if contains == "" {
		writeError(w, http.StatusBadRequest, "contains is required")
		return
	}
	fieldsParam := q.Get("fields")
	if fieldsParam == "" {
		writeError(w, http.StatusBadRequest, "fields is required (comma-separated)")
		return
	}
	fields := strings.Split(fieldsParam, ",")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}

	limit := 200
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	matches, truncated, err := s.storage.FindByValue(contains, fields, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if matches == nil {
		matches = []storage.FindMatch{}
	}

	writeJSON(w, http.StatusOK, KVFindResponse{
		Contains:  contains,
		Fields:    fields,
		Matches:   matches,
		Truncated: truncated,
	})
}

// handleKVSearch handles GET /api/kv/search?contains=&limit=500
func (s *Server) handleKVSearch(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	q := r.URL.Query()
	contains := q.Get("contains")
	if contains == "" {
		writeError(w, http.StatusBadRequest, "contains is required")
		return
	}

	limit := 500
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	keys, truncated, err := s.storage.SearchKeys(contains, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, KVSearchResponse{
		Contains:  contains,
		Keys:      keys,
		Truncated: truncated,
	})
}

// handleKVValueGet handles GET /api/kv/value?key=...
func (s *Server) handleKVValueGet(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	kind, value, exists, err := s.storage.GetKeyInfo(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, KVValueResponse{
		Key:    key,
		Type:   kind,
		Value:  value,
		Exists: exists,
	})
}

// handleKVValuePut handles PUT /api/kv/value with body {key, value}.
// Maintains the file index when the key matches file:<hashId>/...
func (s *Server) handleKVValuePut(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	var req KVValuePutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := s.storage.SetRawValue(req.Key, req.Value); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if hashID := extractFileHashID(req.Key); hashID != "" {
		if err := s.storage.IndexAdd(hashID); err != nil {
			// Non-fatal: the value was written; index entry is best-effort.
			// Surface in the log path of writeJSON via response header.
			w.Header().Set("X-KV-Index-Warning", err.Error())
		}
	}

	writeJSON(w, http.StatusOK, KVValueResponse{
		Key:    req.Key,
		Type:   "string",
		Value:  req.Value,
		Exists: true,
	})
}

// handleKVValueDelete handles DELETE /api/kv/value?key=...
// When the key matches file:<hashId>/..., removes the file index entry if no
// sibling keys remain after the delete.
func (s *Server) handleKVValueDelete(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	key := r.URL.Query().Get("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	deleted, err := s.storage.DeleteRawKey(key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if deleted {
		if hashID := extractFileHashID(key); hashID != "" {
			if _, err := s.storage.IndexRemoveIfEmpty(hashID); err != nil {
				w.Header().Set("X-KV-Index-Warning", err.Error())
			}
		}
	}

	writeJSON(w, http.StatusOK, KVValueDeleteResponse{
		Key:     key,
		Deleted: deleted,
	})
}

// extractFileHashID returns the hashID for a key shaped like file:<hashId>/...
// Returns "" when the key does not follow that convention.
func extractFileHashID(key string) string {
	if !strings.HasPrefix(key, "file:") {
		return ""
	}
	rest := strings.TrimPrefix(key, "file:")
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return ""
	}
	return rest[:slash]
}
