package watcher

import (
	"fmt"
	"log"
	"sync"

	"github.com/metazla/meta-core/internal/storage"
)

const (
	// EventsStream is the Redis Stream name for file events
	EventsStream = "file:events"
	// StreamMaxLen is the approximate max length for the stream
	StreamMaxLen = 10000
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

	// Publish to stream
	_, err := client.XAdd(EventsStream, StreamMaxLen, fields)
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
