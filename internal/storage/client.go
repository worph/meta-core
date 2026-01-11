package storage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps Redis operations for metadata storage
type Client struct {
	client    *redis.Client
	prefix    string
	connected bool
	mu        sync.RWMutex
}

// NewClient creates a new storage client
func NewClient(prefix string) *Client {
	return &Client{
		prefix: prefix,
	}
}

// Connect connects to Redis at the given URL
func (c *Client) Connect(url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Parse Redis URL (redis://host:port)
	addr := strings.TrimPrefix(url, "redis://")

	c.client = redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		PoolSize:     10,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	c.connected = true
	log.Printf("[Storage] Connected to Redis at %s", addr)
	return nil
}

// Close closes the Redis connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client != nil {
		c.connected = false
		return c.client.Close()
	}
	return nil
}

// IsConnected returns true if connected to Redis
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// Health checks if Redis is responding
func (c *Client) Health() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil || !c.connected {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return c.client.Ping(ctx).Err() == nil
}

// buildKey constructs the full key with prefix
func (c *Client) buildKey(key string) string {
	if c.prefix != "" {
		return c.prefix + key
	}
	return key
}

// buildFilePrefix constructs the prefix for a file's metadata keys
func (c *Client) buildFilePrefix(hashID string) string {
	return fmt.Sprintf("/file/%s", hashID)
}

// GetMetadataFlat retrieves all metadata for a file as a flat map
// Key format: /file/{hashId}/{property} -> value
func (c *Client) GetMetadataFlat(hashID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKey(c.buildFilePrefix(hashID))
	result := make(map[string]string)

	// Use SCAN to find all keys with this prefix
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"/*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		if len(keys) > 0 {
			// Get values for all keys
			values, err := c.client.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, fmt.Errorf("mget failed: %w", err)
			}

			for i, key := range keys {
				if values[i] != nil {
					// Strip prefix to get property path
					propPath := strings.TrimPrefix(key, prefix+"/")
					result[propPath] = values[i].(string)
				}
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

// SetMetadataFlat stores metadata for a file as flat key-value pairs
func (c *Client) SetMetadataFlat(hashID string, metadata map[string]string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	if len(metadata) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKey(c.buildFilePrefix(hashID))

	// Use pipeline for batch operations
	pipe := c.client.Pipeline()
	for prop, value := range metadata {
		key := prefix + "/" + prop
		pipe.Set(ctx, key, value, 0)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// GetAllHashIDs returns all unique file hash IDs stored
func (c *Client) GetAllHashIDs() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prefix := c.buildKey("/file/")
	hashSet := make(map[string]bool)

	// Scan for all file keys
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		for _, key := range keys {
			// Extract hashID from key /file/{hashID}/property
			stripped := strings.TrimPrefix(key, prefix)
			parts := strings.SplitN(stripped, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				hashSet[parts[0]] = true
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	// Convert set to slice
	result := make([]string, 0, len(hashSet))
	for hashID := range hashSet {
		result = append(result, hashID)
	}

	return result, nil
}

// GetProperty retrieves a single property value
func (c *Client) GetProperty(hashID, property string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(c.buildFilePrefix(hashID) + "/" + property)
	result, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// SetProperty sets a single property value
func (c *Client) SetProperty(hashID, property, value string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(c.buildFilePrefix(hashID) + "/" + property)
	return c.client.Set(ctx, key, value, 0).Err()
}

// DeleteMetadata deletes all metadata for a file
func (c *Client) DeleteMetadata(hashID string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKey(c.buildFilePrefix(hashID))
	var deleted int64

	// Scan and delete all keys with this prefix
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"/*", 1000).Result()
		if err != nil {
			return deleted, fmt.Errorf("scan failed: %w", err)
		}

		if len(keys) > 0 {
			count, err := c.client.Del(ctx, keys...).Result()
			if err != nil {
				return deleted, fmt.Errorf("del failed: %w", err)
			}
			deleted += count
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return deleted, nil
}

// CountFiles returns the number of unique files stored
func (c *Client) CountFiles() (int, error) {
	hashIDs, err := c.GetAllHashIDs()
	if err != nil {
		return 0, err
	}
	return len(hashIDs), nil
}

// LookupPathByCID searches all file metadata for a matching CID
// Returns the file path if found in poster/posterPath or backdrop/backdropPath
func (c *Client) LookupPathByCID(cid string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	if cid == "" {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Get all hash IDs first
	hashIDs, err := c.getAllHashIDsInternal(ctx)
	if err != nil {
		return "", err
	}

	// For each hash, check poster and backdrop CIDs
	for _, hashID := range hashIDs {
		prefix := c.buildKey(c.buildFilePrefix(hashID))

		// Check poster
		posterCID, err := c.client.Get(ctx, prefix+"/poster").Result()
		if err == nil && posterCID == cid {
			posterPath, err := c.client.Get(ctx, prefix+"/posterPath").Result()
			if err == nil && posterPath != "" {
				return posterPath, nil
			}
		}

		// Check backdrop
		backdropCID, err := c.client.Get(ctx, prefix+"/backdrop").Result()
		if err == nil && backdropCID == cid {
			backdropPath, err := c.client.Get(ctx, prefix+"/backdropPath").Result()
			if err == nil && backdropPath != "" {
				return backdropPath, nil
			}
		}
	}

	return "", nil
}

// getAllHashIDsInternal is an internal version that doesn't acquire locks
// (caller must hold the lock)
func (c *Client) getAllHashIDsInternal(ctx context.Context) ([]string, error) {
	prefix := c.buildKey("/file/")
	hashSet := make(map[string]bool)

	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		for _, key := range keys {
			stripped := strings.TrimPrefix(key, prefix)
			parts := strings.SplitN(stripped, "/", 2)
			if len(parts) > 0 && parts[0] != "" {
				hashSet[parts[0]] = true
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	result := make([]string, 0, len(hashSet))
	for hashID := range hashSet {
		result = append(result, hashID)
	}

	return result, nil
}

// MergeMetadataFlat merges new metadata into existing (PATCH semantics)
// New keys are added, existing keys are updated, missing keys are NOT deleted
func (c *Client) MergeMetadataFlat(hashID string, metadata map[string]string) (int, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	if len(metadata) == 0 {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKey(c.buildFilePrefix(hashID))

	// Use pipeline for batch operations
	pipe := c.client.Pipeline()
	for prop, value := range metadata {
		key := prefix + "/" + prop
		pipe.Set(ctx, key, value, 0)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}

	return len(metadata), nil
}

// DeleteProperty deletes a single property
func (c *Client) DeleteProperty(hashID, property string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(c.buildFilePrefix(hashID) + "/" + property)
	return c.client.Del(ctx, key).Err()
}

// AddToSet adds a value to a set-type field (stored as pipe-delimited string)
// Returns true if value was added, false if it already existed
func (c *Client) AddToSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(c.buildFilePrefix(hashID) + "/" + property)

	// Get current value
	current, err := c.client.Get(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return false, err
	}

	// Parse existing values (pipe-delimited)
	var values []string
	if current != "" {
		values = strings.Split(current, "|")
	}

	// Check if value already exists
	for _, v := range values {
		if v == value {
			return false, nil // Already exists
		}
	}

	// Add new value
	values = append(values, value)
	newValue := strings.Join(values, "|")

	// Save back
	err = c.client.Set(ctx, key, newValue, 0).Err()
	if err != nil {
		return false, err
	}

	return true, nil
}

// RemoveFromSet removes a value from a set-type field
// Returns true if value was removed, false if it didn't exist
func (c *Client) RemoveFromSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKey(c.buildFilePrefix(hashID) + "/" + property)

	// Get current value
	current, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return false, nil // Key doesn't exist
	}
	if err != nil {
		return false, err
	}

	// Parse existing values
	values := strings.Split(current, "|")

	// Find and remove value
	found := false
	newValues := make([]string, 0, len(values))
	for _, v := range values {
		if v == value {
			found = true
		} else {
			newValues = append(newValues, v)
		}
	}

	if !found {
		return false, nil
	}

	// Save back (or delete if empty)
	if len(newValues) == 0 {
		return true, c.client.Del(ctx, key).Err()
	}

	newValue := strings.Join(newValues, "|")
	return true, c.client.Set(ctx, key, newValue, 0).Err()
}
