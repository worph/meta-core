package watcher

import "time"

// FileEventType represents the type of file event
type FileEventType string

const (
	EventTypeAdd    FileEventType = "add"
	EventTypeChange FileEventType = "change"
	EventTypeDelete FileEventType = "delete"
	EventTypeRename FileEventType = "rename"
)

// FileEvent represents a file system event
type FileEvent struct {
	Type        FileEventType `json:"type"`
	Path        string        `json:"path"`        // Relative to FILES_PATH
	Size        int64         `json:"size,omitempty"`
	Timestamp   int64         `json:"timestamp"`
	PartialHash string        `json:"partialHash,omitempty"` // Hash of first 64KB
	OldPath     string        `json:"oldPath,omitempty"`     // For rename events
}

// Subscriber represents a webhook subscriber
type Subscriber struct {
	URL          string   `json:"url"`
	RegisteredAt int64    `json:"registeredAt"`
	EventTypes   []string `json:"eventTypes,omitempty"` // Empty means all
	LastDelivery int64    `json:"lastDelivery,omitempty"`
	FailCount    int      `json:"failCount"`
}

// PendingEvent tracks a file event that's being debounced
type PendingEvent struct {
	Event     FileEvent
	FirstSeen time.Time
	LastSeen  time.Time
	Timer     *time.Timer
}

// SubscribeRequest is the request to register a webhook
type SubscribeRequest struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"eventTypes,omitempty"`
}

// EventsListResponse is the response for listing events
type EventsListResponse struct {
	Events []FileEvent `json:"events"`
	Count  int         `json:"count"`
}

// SubscribersListResponse is the response for listing subscribers
type SubscribersListResponse struct {
	Subscribers []Subscriber `json:"subscribers"`
	Count       int          `json:"count"`
}

// ScanStatusResponse is the response for scan status
type ScanStatusResponse struct {
	Status    string `json:"status"`
	Scanning  bool   `json:"scanning"`
	LastScan  int64  `json:"lastScan,omitempty"`
	FileCount int    `json:"fileCount,omitempty"`
}

// NowMS returns current time in milliseconds
func NowMS() int64 {
	return time.Now().UnixMilli()
}
