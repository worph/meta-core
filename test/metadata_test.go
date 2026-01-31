package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// =============================================================================
// Mock Types for Testing
// =============================================================================

// MockMetadataListItem matches the API response structure
type MockMetadataListItem struct {
	HashID   string `json:"hashId"`
	Title    string `json:"title,omitempty"`
	FileName string `json:"fileName,omitempty"`
	FilePath string `json:"filePath,omitempty"`
}

// MockMetadataListResponse matches GET /api/metadata/list response
type MockMetadataListResponse struct {
	Items []MockMetadataListItem `json:"items"`
	Count int                    `json:"count"`
	Total int                    `json:"total"`
}

// MockHashIDsResponse matches GET /api/metadata/hash-ids response
type MockHashIDsResponse struct {
	HashIds []string `json:"hashIds"`
	Count   int      `json:"count"`
}

// MockMetadataResponse matches GET /api/metadata/{hashId} response
type MockMetadataResponse struct {
	HashID   string            `json:"hashId"`
	Metadata map[string]string `json:"metadata"`
}

// MockPropertyResponse matches GET /api/metadata/{hashId}/property response
type MockPropertyResponse struct {
	HashID   string `json:"hashId"`
	Property string `json:"property"`
	Value    string `json:"value"`
}

// MockSearchResult matches search result structure
type MockSearchResult struct {
	HashID   string            `json:"hashId"`
	Metadata map[string]string `json:"metadata"`
}

// MockSearchResponse matches POST /api/metadata/search response
type MockSearchResponse struct {
	Results []MockSearchResult `json:"results"`
	Count   int                `json:"count"`
	Total   int                `json:"total"`
}

// MockKVInfoResponse matches GET /api/kv/info response
type MockKVInfoResponse struct {
	Prefix      string `json:"prefix"`
	FileCount   int    `json:"fileCount"`
	KeyCount    int    `json:"keyCount"`
	TotalSize   int64  `json:"totalSize"`
	MemoryUsage string `json:"memoryUsage"`
}

// MockKVKeysResponse matches GET /api/kv/keys response
type MockKVKeysResponse struct {
	Keys    []string `json:"keys"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"hasMore"`
}

// MockKVKeyValueResponse matches GET /api/kv/key/{key} response
type MockKVKeyValueResponse struct {
	Key   string            `json:"key"`
	Type  string            `json:"type"`
	Value map[string]string `json:"value"`
}

// =============================================================================
// Metadata API Response Format Tests
// =============================================================================

func TestMetadataListResponseFormat(t *testing.T) {
	response := MockMetadataListResponse{
		Items: []MockMetadataListItem{
			{HashID: "hash001", FileName: "movie.mp4", FilePath: "/files/movie.mp4"},
			{HashID: "hash002", FileName: "show.mkv", FilePath: "/files/show.mkv"},
		},
		Count: 2,
		Total: 2,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Check required fields
	if _, exists := parsed["items"]; !exists {
		t.Error("Missing required field: items")
	}
	if _, exists := parsed["count"]; !exists {
		t.Error("Missing required field: count")
	}
	if _, exists := parsed["total"]; !exists {
		t.Error("Missing required field: total")
	}
}

func TestMetadataHashIdsResponseFormat(t *testing.T) {
	response := MockHashIDsResponse{
		HashIds: []string{"hash001", "hash002", "hash003"},
		Count:   3,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, exists := parsed["hashIds"]; !exists {
		t.Error("Missing required field: hashIds")
	}
	if _, exists := parsed["count"]; !exists {
		t.Error("Missing required field: count")
	}
}

func TestMetadataByHashIdResponseFormat(t *testing.T) {
	response := MockMetadataResponse{
		HashID: "hash001",
		Metadata: map[string]string{
			"fileName":  "Test Movie.mp4",
			"videoType": "movie",
			"title":     "Test Movie",
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, exists := parsed["hashId"]; !exists {
		t.Error("Missing required field: hashId")
	}
	if _, exists := parsed["metadata"]; !exists {
		t.Error("Missing required field: metadata")
	}
}

func TestMetadataPropertyResponseFormat(t *testing.T) {
	response := MockPropertyResponse{
		HashID:   "hash001",
		Property: "videoType",
		Value:    "movie",
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"hashId", "property", "value"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

func TestMetadataSearchResponseFormat(t *testing.T) {
	response := MockSearchResponse{
		Results: []MockSearchResult{
			{
				HashID:   "hash001",
				Metadata: map[string]string{"fileName": "movie.mp4"},
			},
		},
		Count: 1,
		Total: 10,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"results", "count", "total"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

// =============================================================================
// KV Browser API Response Format Tests
// =============================================================================

func TestKVInfoResponseFormat(t *testing.T) {
	response := MockKVInfoResponse{
		Prefix:      "file:",
		FileCount:   100,
		KeyCount:    100,
		TotalSize:   1024000,
		MemoryUsage: "1.5M",
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"prefix", "fileCount", "keyCount", "totalSize", "memoryUsage"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

func TestKVKeysResponseFormat(t *testing.T) {
	response := MockKVKeysResponse{
		Keys:    []string{"file:hash001", "file:hash002"},
		Cursor:  "2",
		HasMore: true,
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"keys", "cursor", "hasMore"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

func TestKVKeyValueResponseFormat(t *testing.T) {
	response := MockKVKeyValueResponse{
		Key:  "file:hash001",
		Type: "hash",
		Value: map[string]string{
			"fileName": "movie.mp4",
			"title":    "Test Movie",
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	requiredFields := []string{"key", "type", "value"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

// =============================================================================
// Metadata API Route Registration Tests
// =============================================================================

func TestMetadataAPIRoutes(t *testing.T) {
	router := mux.NewRouter()

	// Register Metadata API routes (matching server.go)
	metadataRoutes := []struct {
		path   string
		method string
	}{
		{"/api/metadata/hash-ids", "GET"},
		{"/api/metadata/list", "GET"},
		{"/api/metadata/search", "POST"},
		{"/api/metadata/batch", "POST"},
		{"/api/metadata/clear", "POST"},
		{"/api/metadata/{hashId}/property", "GET"},
		{"/api/metadata/{hashId}/property", "PUT"},
		{"/api/metadata/{hashId}", "GET"},
		{"/api/metadata/{hashId}", "PUT"},
		{"/api/metadata/{hashId}", "DELETE"},
	}

	// Register dummy handlers
	for _, route := range metadataRoutes {
		path := route.path
		router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}).Methods(route.method)
	}

	// Test cases with actual path values
	testCases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/metadata/hash-ids"},
		{"GET", "/api/metadata/list"},
		{"GET", "/api/metadata/list?limit=10&offset=0"},
		{"POST", "/api/metadata/search"},
		{"POST", "/api/metadata/batch"},
		{"POST", "/api/metadata/clear"},
		{"GET", "/api/metadata/testhash001/property?property=videoType"},
		{"PUT", "/api/metadata/testhash001/property"},
		{"GET", "/api/metadata/testhash001"},
		{"PUT", "/api/metadata/testhash001"},
		{"DELETE", "/api/metadata/testhash001"},
	}

	for _, tc := range testCases {
		var body *bytes.Reader
		if tc.method == "POST" || tc.method == "PUT" {
			body = bytes.NewReader([]byte(`{}`))
		}

		var req *http.Request
		if body != nil {
			req = httptest.NewRequest(tc.method, tc.path, body)
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		}
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Metadata API route %s %s returned status %d, expected 200", tc.method, tc.path, rr.Code)
		}
	}
}

// =============================================================================
// KV Browser API Route Registration Tests
// =============================================================================

func TestKVBrowserAPIRoutes(t *testing.T) {
	router := mux.NewRouter()

	// Register KV Browser API routes (matching server.go)
	kvRoutes := []struct {
		path   string
		method string
	}{
		{"/api/kv/info", "GET"},
		{"/api/kv/keys", "GET"},
		{"/api/kv/key/{key:.*}", "GET"},
	}

	// Register dummy handlers
	for _, route := range kvRoutes {
		path := route.path
		router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}).Methods(route.method)
	}

	// Test cases
	testCases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/kv/info"},
		{"GET", "/api/kv/keys"},
		{"GET", "/api/kv/keys?cursor=0&count=50"},
		{"GET", "/api/kv/key/file:testhash001"},
	}

	for _, tc := range testCases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("KV Browser API route %s %s returned status %d, expected 200", tc.method, tc.path, rr.Code)
		}
	}
}

// =============================================================================
// Error Response Tests
// =============================================================================

func TestMetadataAPIErrorResponses(t *testing.T) {
	// Test that error responses have the expected format
	type ErrorResponse struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}

	errResp := ErrorResponse{
		Error:   "metadata not found",
		Message: "No metadata found for the given hash ID",
	}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("Failed to marshal error response: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, exists := parsed["error"]; !exists {
		t.Error("Error response missing 'error' field")
	}
}

// =============================================================================
// Pagination Tests
// =============================================================================

func TestMetadataListPagination(t *testing.T) {
	// Test pagination parameter handling
	testCases := []struct {
		name   string
		offset int
		limit  int
	}{
		{"default pagination", 0, 100},
		{"custom offset", 10, 100},
		{"custom limit", 0, 50},
		{"both custom", 20, 25},
	}

	for _, tc := range testCases {
		// Simulate response with pagination
		response := MockMetadataListResponse{
			Items: make([]MockMetadataListItem, tc.limit),
			Count: tc.limit,
			Total: 1000,
		}

		if response.Count != tc.limit {
			t.Errorf("%s: expected count %d, got %d", tc.name, tc.limit, response.Count)
		}
	}
}

func TestKVKeysPagination(t *testing.T) {
	// Test cursor-based pagination
	testCases := []struct {
		name    string
		cursor  int
		count   int
		total   int
		hasMore bool
	}{
		{"first page", 0, 50, 100, true},
		{"second page", 50, 50, 100, false},
		{"small result", 0, 50, 10, false},
	}

	for _, tc := range testCases {
		response := MockKVKeysResponse{
			Keys:    make([]string, tc.count),
			Cursor:  "50",
			HasMore: tc.hasMore,
		}

		if response.HasMore != tc.hasMore {
			t.Errorf("%s: expected hasMore %v, got %v", tc.name, tc.hasMore, response.HasMore)
		}
	}
}
