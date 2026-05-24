package events

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// MetaEventsStream is the Redis stream name for metadata events
	MetaEventsStream = "meta:events"

	// MetaEventsMaxLen is the approximate cap on stream length. Bounded
	// retention is a contract requirement for the SSE wire — see
	// docs/api-mediated-access.md "Backing-store retention and lifecycle".
	// Cheap (~) trim keeps growth bounded without serialising writes.
	MetaEventsMaxLen = 100_000
)

// MetaPublisher subscribes to Redis keyspace notifications
// and publishes metadata change events to the meta:events stream
type MetaPublisher struct {
	client  *redis.Client
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
	mu      sync.RWMutex
}

// NewMetaPublisher creates a new MetaPublisher
func NewMetaPublisher(client *redis.Client) *MetaPublisher {
	ctx, cancel := context.WithCancel(context.Background())
	return &MetaPublisher{
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start begins subscribing to keyspace notifications.
//
// The stream is NOT truncated on startup — the SSE wire contract requires
// Last-Event-ID resumption to survive a meta-core restart, which means
// entries must persist across restart. Growth is bounded instead by
// MAXLEN ~ MetaEventsMaxLen on every XAdd (see publishEvent).
func (p *MetaPublisher) Start() error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = true
	p.mu.Unlock()

	p.wg.Add(1)
	go p.subscribeLoop()

	log.Println("[MetaPublisher] Started - listening for keyspace notifications")
	return nil
}

// Stop stops the publisher
func (p *MetaPublisher) Stop() error {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = false
	p.mu.Unlock()

	p.cancel()
	p.wg.Wait()

	log.Println("[MetaPublisher] Stopped")
	return nil
}

// IsRunning returns true if the publisher is running
func (p *MetaPublisher) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// subscribeLoop subscribes to keyspace notifications and publishes events
func (p *MetaPublisher) subscribeLoop() {
	defer p.wg.Done()

	// Subscribe to all file:* keyspace events
	// Pattern: __keyspace@0__:file:*
	pattern := "__keyspace@0__:file:*"
	pubsub := p.client.PSubscribe(p.ctx, pattern)
	defer pubsub.Close()

	log.Printf("[MetaPublisher] Subscribed to pattern: %s", pattern)

	ch := pubsub.Channel()
	for {
		select {
		case <-p.ctx.Done():
			return
		case msg := <-ch:
			if msg == nil {
				continue
			}
			// msg.Channel: __keyspace@0__:file:midhash256:abc123/tmdb
			// msg.Payload: set, del, expire, etc.
			p.publishEvent(msg.Channel, msg.Payload)
		}
	}
}

// publishEvent publishes a keyspace event to the meta:events stream
func (p *MetaPublisher) publishEvent(channel, operation string) {
	// Extract key from channel: __keyspace@0__:file:xxx/field -> file:xxx/field
	key := strings.TrimPrefix(channel, "__keyspace@0__:")

	// Skip index key and non-file keys
	if strings.Contains(key, "__index__") || !strings.HasPrefix(key, "file:") {
		return
	}

	// Build event fields
	fields := map[string]interface{}{
		"type": operation, // "set", "del", "expire"
		"key":  key,       // "file:midhash256:abc123/tmdb"
		"ts":   fmt.Sprintf("%d", time.Now().UnixMilli()),
	}

	// Approximate MAXLEN trim keeps the stream bounded across restarts
	// (see Start). Cap is intentionally generous; most consumers reconnect
	// well within retention.
	_, err := p.client.XAdd(p.ctx, &redis.XAddArgs{
		Stream: MetaEventsStream,
		MaxLen: MetaEventsMaxLen,
		Approx: true,
		Values: fields,
	}).Result()

	if err != nil {
		log.Printf("[MetaPublisher] Error publishing event: %v", err)
	}
}

// publishEventDirect publishes an event directly (not from keyspace notification)
func (p *MetaPublisher) publishEventDirect(operation, key string) {
	fields := map[string]interface{}{
		"type": operation,
		"key":  key,
		"ts":   fmt.Sprintf("%d", time.Now().UnixMilli()),
	}

	_, err := p.client.XAdd(p.ctx, &redis.XAddArgs{
		Stream: MetaEventsStream,
		MaxLen: MetaEventsMaxLen,
		Approx: true,
		Values: fields,
	}).Result()

	if err != nil {
		log.Printf("[MetaPublisher] Error publishing direct event: %v", err)
	}
}

// RepublishFunc is a function type for getting metadata that can be republished
type RepublishFunc func() (hashIDs []string, getMetadata func(hashID string) (map[string]string, error), err error)

// RepublishAllMetadata iterates over all existing file metadata and publishes events
// This is called on startup and can be triggered via API
func (p *MetaPublisher) RepublishAllMetadata(republishFunc RepublishFunc) (int, error) {
	p.mu.RLock()
	running := p.running
	p.mu.RUnlock()

	if !running {
		return 0, fmt.Errorf("publisher not running")
	}

	// Get all hash IDs and metadata getter
	hashIDs, getMetadata, err := republishFunc()
	if err != nil {
		return 0, fmt.Errorf("failed to get hash IDs: %w", err)
	}

	log.Printf("[MetaPublisher] Republishing events for %d files...", len(hashIDs))

	publishedCount := 0
	for _, hashID := range hashIDs {
		// Get all properties for this file
		metadata, err := getMetadata(hashID)
		if err != nil || metadata == nil {
			continue
		}

		// Publish a set event for each property
		for property := range metadata {
			key := fmt.Sprintf("file:%s/%s", hashID, property)
			p.publishEventDirect("set", key)
			publishedCount++
		}
	}

	log.Printf("[MetaPublisher] Republished %d property events", publishedCount)
	return publishedCount, nil
}
