package cache

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// EventsStream is the Redis Stream name for file events
	EventsStream = "file:events"

	// ConsumerGroup is the consumer group name for cache invalidation
	ConsumerGroup = "cache-invalidator"

	// ConsumerName identifies this consumer in the group
	ConsumerName = "meta-core-cache"
)

// Invalidator subscribes to Redis Stream for file events and invalidates cache
type Invalidator struct {
	manager      *Manager
	redisClient  *redis.Client
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	lastID       string
}

// NewInvalidator creates a new cache invalidator
func NewInvalidator(manager *Manager, redisClient *redis.Client) *Invalidator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Invalidator{
		manager:     manager,
		redisClient: redisClient,
		ctx:         ctx,
		cancel:      cancel,
		lastID:      "$", // Start from new messages
	}
}

// Start starts listening to the Redis Stream
func (inv *Invalidator) Start() error {
	if inv.manager == nil || !inv.manager.Enabled() {
		log.Println("[CacheInvalidator] Cache disabled, not starting invalidator")
		return nil
	}

	if inv.redisClient == nil {
		log.Println("[CacheInvalidator] Redis client not available, not starting invalidator")
		return nil
	}

	// Try to create consumer group (ignore error if already exists)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := inv.redisClient.XGroupCreateMkStream(ctx, EventsStream, ConsumerGroup, "$").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		// Log but don't fail - we can still use XREAD
		log.Printf("[CacheInvalidator] Note: consumer group creation: %v (using XREAD fallback)", err)
	}

	inv.wg.Add(1)
	go inv.listen()

	log.Println("[CacheInvalidator] Started listening on file:events stream")
	return nil
}

// Stop stops the invalidator
func (inv *Invalidator) Stop() error {
	log.Println("[CacheInvalidator] Stopping...")
	inv.cancel()
	inv.wg.Wait()
	return nil
}

// listen reads from the Redis Stream and processes events
func (inv *Invalidator) listen() {
	defer inv.wg.Done()

	for {
		select {
		case <-inv.ctx.Done():
			return
		default:
			inv.readEvents()
		}
	}
}

// readEvents reads and processes a batch of events
func (inv *Invalidator) readEvents() {
	ctx, cancel := context.WithTimeout(inv.ctx, 5*time.Second)
	defer cancel()

	// Use XREAD with BLOCK for efficient waiting
	streams, err := inv.redisClient.XRead(ctx, &redis.XReadArgs{
		Streams: []string{EventsStream, inv.lastID},
		Count:   100,
		Block:   2 * time.Second,
	}).Result()

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded || err == redis.Nil {
			return // Normal timeout or shutdown
		}
		log.Printf("[CacheInvalidator] Error reading stream: %v", err)
		time.Sleep(1 * time.Second) // Backoff on error
		return
	}

	for _, stream := range streams {
		for _, msg := range stream.Messages {
			inv.processEvent(msg)
			inv.lastID = msg.ID
		}
	}
}

// processEvent processes a single file event
func (inv *Invalidator) processEvent(msg redis.XMessage) {
	eventType, _ := msg.Values["type"].(string)
	path, _ := msg.Values["path"].(string)

	switch eventType {
	case "delete":
		// File deleted - invalidate cache
		if path != "" {
			log.Printf("[CacheInvalidator] Invalidating deleted file: %s", path)
			inv.manager.Invalidate(path)
		}

	case "change":
		// File changed - invalidate cache
		if path != "" {
			log.Printf("[CacheInvalidator] Invalidating changed file: %s", path)
			inv.manager.Invalidate(path)
		}

	case "rename":
		// File renamed - invalidate old path
		oldPath, _ := msg.Values["oldPath"].(string)
		if oldPath != "" {
			log.Printf("[CacheInvalidator] Invalidating renamed file (old): %s", oldPath)
			inv.manager.Invalidate(oldPath)
		}
		// Also invalidate new path in case it was cached before
		if path != "" {
			inv.manager.Invalidate(path)
		}

	case "reset":
		// Full reset - clear entire cache
		log.Println("[CacheInvalidator] Reset event received - clearing cache")
		inv.manager.Clear()

	case "add":
		// New file - nothing to invalidate
		// (file wouldn't be in cache yet)
	}
}
