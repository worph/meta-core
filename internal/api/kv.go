package api

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
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
