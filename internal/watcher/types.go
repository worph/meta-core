package watcher

import "time"

// FileEventType represents the type of file event
type FileEventType string

const (
	EventTypeAdd    FileEventType = "add"
	EventTypeChange FileEventType = "change"
	EventTypeDelete FileEventType = "delete"
	EventTypeRename FileEventType = "rename"
	EventTypeReset  FileEventType = "reset"
)

// FileEvent represents a file system event
type FileEvent struct {
	Type       FileEventType `json:"type"`
	Path       string        `json:"path"`                 // Relative to FILES_PATH
	Size       int64         `json:"size,omitempty"`
	Timestamp  int64         `json:"timestamp"`
	MidHash256 string        `json:"midhash256,omitempty"` // midhash256 CID (SHA-256 of middle 1MB + file size)
	OldPath    string        `json:"oldPath,omitempty"`    // For rename events
	WatcherID  string        `json:"watcherId,omitempty"`  // For reset events
}

// PendingEvent tracks a file event that's being debounced
type PendingEvent struct {
	Event     FileEvent
	FirstSeen time.Time
	LastSeen  time.Time
	Timer     *time.Timer
}

// EventsListResponse is the response for listing events
type EventsListResponse struct {
	Events []FileEvent `json:"events"`
	Count  int         `json:"count"`
}

// ScanStatusResponse is the response for scan status
type ScanStatusResponse struct {
	Status    string `json:"status"`
	Scanning  bool   `json:"scanning"`
	LastScan  int64  `json:"lastScan,omitempty"`
	FileCount int    `json:"fileCount,omitempty"`
}

// DiffScanResult holds the results of a differential scan
type DiffScanResult struct {
	Added   int // Number of new files added
	Changed int // Number of files with changed size/mtime
	Deleted int // Number of files that no longer exist
	Total   int // Total number of files currently tracked
}

// NowMS returns current time in milliseconds
func NowMS() int64 {
	return time.Now().UnixMilli()
}
