package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/metazla/meta-core/internal/leader"
)

func TestLeaderLockInfoJSON(t *testing.T) {
	info := &leader.LeaderLockInfo{
		Host:      "test-host",
		API:       "redis://10.0.0.1:6379",
		HTTP:      "http://10.0.0.1:8180",
		BaseURL:   "http://localhost:8180",
		Timestamp: 1704067200000,
		PID:       12345,
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify JSON structure matches TypeScript format
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed["host"] != "test-host" {
		t.Errorf("Expected host 'test-host', got '%v'", parsed["host"])
	}

	if parsed["api"] != "redis://10.0.0.1:6379" {
		t.Errorf("Expected api 'redis://10.0.0.1:6379', got '%v'", parsed["api"])
	}

	if parsed["http"] != "http://10.0.0.1:8180" {
		t.Errorf("Expected http 'http://10.0.0.1:8180', got '%v'", parsed["http"])
	}

	if parsed["baseUrl"] != "http://localhost:8180" {
		t.Errorf("Expected baseUrl 'http://localhost:8180', got '%v'", parsed["baseUrl"])
	}

	// Timestamp should be a number
	if _, ok := parsed["timestamp"].(float64); !ok {
		t.Errorf("Expected timestamp to be a number, got %T", parsed["timestamp"])
	}

	// PID should be a number
	if _, ok := parsed["pid"].(float64); !ok {
		t.Errorf("Expected pid to be a number, got %T", parsed["pid"])
	}
}

func TestLeaderLockInfoJSONOmitEmpty(t *testing.T) {
	info := &leader.LeaderLockInfo{
		Host:      "test-host",
		API:       "redis://10.0.0.1:6379",
		HTTP:      "http://10.0.0.1:8180",
		BaseURL:   "", // Empty - should be omitted
		Timestamp: 1704067200000,
		PID:       12345,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// baseUrl should not be present when empty
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if _, exists := parsed["baseUrl"]; exists {
		t.Error("Expected baseUrl to be omitted when empty")
	}
}

func TestLeaderInfoFileFormat(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "meta-core-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	info := &leader.LeaderLockInfo{
		Host:      "test-host",
		API:       "redis://10.0.0.1:6379",
		HTTP:      "http://10.0.0.1:8180",
		Timestamp: 1704067200000,
		PID:       12345,
	}

	// Write to file
	infoPath := filepath.Join(tmpDir, "kv-leader.info")
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	if err := os.WriteFile(infoPath, data, 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	// Read and parse
	readData, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	var parsed leader.LeaderLockInfo
	if err := json.Unmarshal(readData, &parsed); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if parsed.Host != info.Host {
		t.Errorf("Host mismatch: expected '%s', got '%s'", info.Host, parsed.Host)
	}

	if parsed.API != info.API {
		t.Errorf("API mismatch: expected '%s', got '%s'", info.API, parsed.API)
	}

	if parsed.HTTP != info.HTTP {
		t.Errorf("HTTP mismatch: expected '%s', got '%s'", info.HTTP, parsed.HTTP)
	}

	if parsed.Timestamp != info.Timestamp {
		t.Errorf("Timestamp mismatch: expected %d, got %d", info.Timestamp, parsed.Timestamp)
	}

	if parsed.PID != info.PID {
		t.Errorf("PID mismatch: expected %d, got %d", info.PID, parsed.PID)
	}
}
