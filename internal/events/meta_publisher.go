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
	// StreamMaxLen is the maximum stream length (approximate)
	StreamMaxLen = 10000
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

// Start begins subscribing to keyspace notifications
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

	// Add to stream
	_, err := p.client.XAdd(p.ctx, &redis.XAddArgs{
		Stream: MetaEventsStream,
		MaxLen: StreamMaxLen,
		Approx: true,
		Values: fields,
	}).Result()

	if err != nil {
		log.Printf("[MetaPublisher] Error publishing event: %v", err)
	}
}
