package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// MockHealthResponse represents the expected health response structure
type MockHealthResponse struct {
	Status    string `json:"status"`
	Role      string `json:"role"`
	Redis     bool   `json:"redis"`
	Timestamp string `json:"timestamp"`
}

func TestHealthResponseFormat(t *testing.T) {
	// Test that the health response has the expected format
	response := MockHealthResponse{
		Status:    "ok",
		Role:      "leader",
		Redis:     true,
		Timestamp: "2024-01-01T00:00:00Z",
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
	requiredFields := []string{"status", "role", "redis", "timestamp"}
	for _, field := range requiredFields {
		if _, exists := parsed[field]; !exists {
			t.Errorf("Missing required field: %s", field)
		}
	}
}

func TestRouterSetup(t *testing.T) {
	router := mux.NewRouter()

	// Register routes (simulating what the server does)
	routes := []struct {
		path   string
		method string
	}{
		{"/health", "GET"},
		{"/status", "GET"},
		{"/leader", "GET"},
		{"/role", "GET"},
		{"/meta/{hash}", "GET"},
		{"/meta/{hash}", "PUT"},
		{"/meta/{hash}", "DELETE"},
		{"/meta", "GET"},
		{"/data/{hash}/path", "GET"},
		{"/data/{hash}", "HEAD"},
		{"/services", "GET"},
		{"/services/{name}", "GET"},
	}

	// Register dummy handlers
	for _, route := range routes {
		path := route.path
		router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).Methods(route.method)
	}

	// Test that routes are registered
	testCases := []struct {
		method string
		path   string
	}{
		{"GET", "/health"},
		{"GET", "/status"},
		{"GET", "/leader"},
		{"GET", "/role"},
		{"GET", "/meta/testhash123"},
		{"PUT", "/meta/testhash123"},
		{"DELETE", "/meta/testhash123"},
		{"GET", "/meta"},
		{"GET", "/data/testhash123/path"},
		{"HEAD", "/data/testhash123"},
		{"GET", "/services"},
		{"GET", "/services/meta-sort"},
	}

	for _, tc := range testCases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Route %s %s returned status %d, expected 200", tc.method, tc.path, rr.Code)
		}
	}
}

func TestCORSHeaders(t *testing.T) {
	// Create a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with CORS middleware
	corsHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()

	corsHandler(handler).ServeHTTP(rr, req)

	// Check CORS headers
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("Missing or incorrect Access-Control-Allow-Origin header")
	}

	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("Missing Access-Control-Allow-Methods header")
	}
}

func TestOptionsRequest(t *testing.T) {
	corsHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("OPTIONS", "/meta/test", nil)
	rr := httptest.NewRecorder()

	corsHandler(handler).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("OPTIONS request returned status %d, expected 200", rr.Code)
	}
}
