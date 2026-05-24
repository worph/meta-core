package watcher

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/metazla/meta-core/internal/storage"
)

const (
	// EventsStream is the Redis Stream name for file events
	EventsStream = "file:events"

	// EventsStreamMaxLen is the approximate cap on stream length. Bounded
	// retention is a contract requirement for the SSE wire — see
	// docs/api-mediated-access.md "Backing-store retention and lifecycle".
	EventsStreamMaxLen = 100_000
)

// Dispatcher sends file events to Redis Stream
type Dispatcher struct {
	mu            sync.RWMutex
	storageClient *storage.Client
}

// NewDispatcher creates a new event dispatcher
func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// SetStorageClient sets the storage client for Redis Stream publishing
func (d *Dispatcher) SetStorageClient(client *storage.Client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.storageClient = client
	log.Println("[Dispatcher] Storage client set for Redis Stream publishing")
}

// Dispatch sends an event to Redis Stream
func (d *Dispatcher) Dispatch(event FileEvent) {
	go d.dispatchToStream(event)
}

// dispatchToStream publishes event to Redis Stream for service consumption
func (d *Dispatcher) dispatchToStream(event FileEvent) {
	d.mu.RLock()
	client := d.storageClient
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return
	}

	// Build stream fields
	fields := map[string]interface{}{
		"type":      string(event.Type),
		"path":      event.Path,
		"timestamp": fmt.Sprintf("%d", event.Timestamp),
	}

	// Add optional fields
	if event.Size > 0 {
		fields["size"] = fmt.Sprintf("%d", event.Size)
	}
	if event.MidHash256 != "" {
		fields["midhash256"] = event.MidHash256
	}
	if event.OldPath != "" {
		fields["oldPath"] = event.OldPath
	}
	if event.WatcherID != "" {
		fields["watcherId"] = event.WatcherID
	}

	// Approximate MAXLEN trim keeps the stream bounded. The deliberate
	// `EmitReset → ClearStream` path is still the only way the stream gets
	// truncated to zero (signalled to consumers via the `reset` event).
	_, err := client.XAdd(EventsStream, EventsStreamMaxLen, fields)
	if err != nil {
		log.Printf("[Dispatcher] Failed to publish to stream: %v", err)
	}
}

// EmitReset clears the stream and emits a reset event
// This is called when triggering a full rescan
func (d *Dispatcher) EmitReset(watcherID string) error {
	d.mu.RLock()
	client := d.storageClient
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return fmt.Errorf("storage client not connected")
	}

	// Delete the stream to clear all historical events
	if err := client.ClearStream(EventsStream); err != nil {
		log.Printf("[Dispatcher] Warning: failed to clear stream: %v", err)
		// Continue anyway - we'll emit the reset event
	}

	log.Printf("[Dispatcher] Cleared stream %s for reset", EventsStream)

	// Emit reset event as the first message in the fresh stream
	event := FileEvent{
		Type:      EventTypeReset,
		Path:      "",  // No specific path for reset
		WatcherID: watcherID,
		Timestamp: NowMS(),
	}

	// Dispatch synchronously for reset
	d.dispatchToStream(event)

	return nil
}

// GetRecentEvents reads recent events from the Redis stream, newest first.
// If sinceMS > 0, only events strictly newer than that timestamp are returned.
// limit caps the number of returned events (defaults to 100 when <= 0).
func (d *Dispatcher) GetRecentEvents(sinceMS int64, limit int) ([]FileEvent, error) {
	d.mu.RLock()
	client := d.storageClient
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return []FileEvent{}, nil
	}

	if limit <= 0 {
		limit = 100
	}

	start := "+"
	stop := "-"
	if sinceMS > 0 {
		// Exclusive lower bound: skip entries at or before sinceMS.
		stop = fmt.Sprintf("(%d-0", sinceMS)
	}

	entries, err := client.XRevRange(EventsStream, start, stop, int64(limit))
	if err != nil {
		return nil, err
	}

	events := make([]FileEvent, 0, len(entries))
	for _, entry := range entries {
		events = append(events, parseStreamEntry(entry))
	}
	return events, nil
}

// parseStreamEntry converts a Redis stream entry back into a FileEvent.
// All fields are stored as strings by XADD, so numerics need parsing.
func parseStreamEntry(entry storage.StreamEntry) FileEvent {
	event := FileEvent{}

	if v, ok := entry.Values["type"].(string); ok {
		event.Type = FileEventType(v)
	}
	if v, ok := entry.Values["path"].(string); ok {
		event.Path = v
	}
	if v, ok := entry.Values["timestamp"].(string); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			event.Timestamp = n
		}
	}
	if v, ok := entry.Values["size"].(string); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			event.Size = n
		}
	}
	if v, ok := entry.Values["midhash256"].(string); ok {
		event.MidHash256 = v
	}
	if v, ok := entry.Values["oldPath"].(string); ok {
		event.OldPath = v
	}
	if v, ok := entry.Values["watcherId"].(string); ok {
		event.WatcherID = v
	}

	// Fall back to the stream ID's millisecond prefix if the timestamp
	// field is missing or unparseable — every Redis stream ID starts with ms.
	if event.Timestamp == 0 && entry.ID != "" {
		if dash := strings.IndexByte(entry.ID, '-'); dash > 0 {
			if n, err := strconv.ParseInt(entry.ID[:dash], 10, 64); err == nil {
				event.Timestamp = n
			}
		}
	}

	return event
}

// GetStreamLength returns the current stream length
func (d *Dispatcher) GetStreamLength() (int64, error) {
	d.mu.RLock()
	client := d.storageClient
	d.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return 0, fmt.Errorf("storage client not connected")
	}

	return client.GetStreamLength(EventsStream)
}
