package watchers

import "time"

const (
	// MinIntervalMs is the minimum polling interval (5 seconds)
	MinIntervalMs = 5000
	// DefaultIntervalMs is the default polling interval (30 seconds)
	DefaultIntervalMs = 30000
)

// WatcherConfig represents a watcher configuration stored in watchers.json
type WatcherConfig struct {
	ID         string `json:"id"`
	Path       string `json:"path"`       // e.g., "/files/watch", "/files/movies"
	IntervalMs int    `json:"intervalMs"` // Min: 5000 (5s), Default: 30000 (30s)
	Enabled    bool   `json:"enabled"`
	CreatedAt  int64  `json:"createdAt"`
}

// WatcherStatus includes runtime status in API responses
type WatcherStatus struct {
	WatcherConfig
	Active     bool `json:"active"`     // Polling goroutine running
	LastScan   int64 `json:"lastScan"`   // Unix ms timestamp
	FileCount  int   `json:"fileCount"`  // Files found in last scan
	IsScanning bool  `json:"isScanning"` // Currently scanning
}

// WatchersFile is the structure of watchers.json
type WatchersFile struct {
	Version  int             `json:"version"`
	Watchers []WatcherConfig `json:"watchers"`
}

// CreateWatcherRequest is the request body for creating a watcher
type CreateWatcherRequest struct {
	Path       string `json:"path"`
	IntervalMs int    `json:"intervalMs"`
	Enabled    bool   `json:"enabled"`
}

// UpdateWatcherRequest is the request body for updating a watcher
type UpdateWatcherRequest struct {
	Path       *string `json:"path,omitempty"`
	IntervalMs *int    `json:"intervalMs,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
}

// WatchersListResponse is the API response for listing watchers
type WatchersListResponse struct {
	Watchers []WatcherStatus `json:"watchers"`
	Count    int             `json:"count"`
}

// NowMS returns current time in milliseconds
func NowMS() int64 {
	return time.Now().UnixMilli()
}
