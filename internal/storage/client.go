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

// buildHashKey constructs the Redis Hash key for a file
func (c *Client) buildHashKey(hashID string) string {
	return c.buildKey(fmt.Sprintf("file:%s", hashID))
}

// buildIndexKey constructs the key for the file index set
func (c *Client) buildIndexKey() string {
	return c.buildKey("file:__index__")
}

// GetMetadataFlat retrieves all metadata for a file as a flat map
// Uses Redis Hash: HGETALL file:{hashId}
func (c *Client) GetMetadataFlat(hashID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)
	result, err := c.client.HGetAll(ctx, hashKey).Result()
	if err != nil {
		return nil, fmt.Errorf("hgetall failed: %w", err)
	}

	if len(result) == 0 {
		return nil, nil
	}

	return result, nil
}

// SetMetadataFlat stores metadata for a file using Redis Hash
// Uses Redis Hash: HMSET file:{hashId} prop1 val1 prop2 val2...
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

	hashKey := c.buildHashKey(hashID)

	// Use HMSET to set all fields at once
	if err := c.client.HMSet(ctx, hashKey, metadata).Err(); err != nil {
		return fmt.Errorf("hmset failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return fmt.Errorf("sadd index failed: %w", err)
	}

	return nil
}

// GetAllHashIDs returns all unique file hash IDs stored
// Uses Redis Set: SMEMBERS file:__index__
func (c *Client) GetAllHashIDs() ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	indexKey := c.buildIndexKey()
	result, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}

	return result, nil
}

// GetProperty retrieves a single property value
// Uses Redis Hash: HGET file:{hashId} {property}
func (c *Client) GetProperty(hashID, property string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)
	result, err := c.client.HGet(ctx, hashKey, property).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// SetProperty sets a single property value
// Uses Redis Hash: HSET file:{hashId} {property} {value}
func (c *Client) SetProperty(hashID, property, value string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)

	// Use HSET to set the field
	if err := c.client.HSet(ctx, hashKey, property, value).Err(); err != nil {
		return fmt.Errorf("hset failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return fmt.Errorf("sadd index failed: %w", err)
	}

	return nil
}

// DeleteMetadata deletes all metadata for a file
// Uses Redis Hash: DEL file:{hashId}
func (c *Client) DeleteMetadata(hashID string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)

	// Get field count before deletion
	fieldCount, err := c.client.HLen(ctx, hashKey).Result()
	if err != nil {
		return 0, fmt.Errorf("hlen failed: %w", err)
	}

	// Delete the hash
	if err := c.client.Del(ctx, hashKey).Err(); err != nil {
		return 0, fmt.Errorf("del failed: %w", err)
	}

	// Remove from index set
	if err := c.client.SRem(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return fieldCount, fmt.Errorf("srem index failed: %w", err)
	}

	return fieldCount, nil
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

	// For each hash, check poster and backdrop CIDs using Hash operations
	for _, hashID := range hashIDs {
		hashKey := c.buildHashKey(hashID)

		// Check poster
		posterCID, err := c.client.HGet(ctx, hashKey, "poster").Result()
		if err == nil && posterCID == cid {
			posterPath, err := c.client.HGet(ctx, hashKey, "posterPath").Result()
			if err == nil && posterPath != "" {
				return posterPath, nil
			}
		}

		// Check backdrop
		backdropCID, err := c.client.HGet(ctx, hashKey, "backdrop").Result()
		if err == nil && backdropCID == cid {
			backdropPath, err := c.client.HGet(ctx, hashKey, "backdropPath").Result()
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
	indexKey := c.buildIndexKey()
	result, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers failed: %w", err)
	}
	return result, nil
}

// MergeMetadataFlat merges new metadata into existing (PATCH semantics)
// New keys are added, existing keys are updated, missing keys are NOT deleted
// Uses Redis Hash: HMSET file:{hashId}
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

	hashKey := c.buildHashKey(hashID)

	// Use HMSET to merge fields
	if err := c.client.HMSet(ctx, hashKey, metadata).Err(); err != nil {
		return 0, fmt.Errorf("hmset failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return len(metadata), fmt.Errorf("sadd index failed: %w", err)
	}

	return len(metadata), nil
}

// DeleteProperty deletes a single property
// Uses Redis Hash: HDEL file:{hashId} {property}
func (c *Client) DeleteProperty(hashID, property string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)
	return c.client.HDel(ctx, hashKey, property).Err()
}

// AddToSet adds a value to a set-type field (stored as pipe-delimited string in Hash field)
// Returns true if value was added, false if it already existed
func (c *Client) AddToSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)

	// Get current value
	current, err := c.client.HGet(ctx, hashKey, property).Result()
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

	// Save back using HSET
	if err := c.client.HSet(ctx, hashKey, property, newValue).Err(); err != nil {
		return false, err
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return true, fmt.Errorf("sadd index failed: %w", err)
	}

	return true, nil
}

// RemoveFromSet removes a value from a set-type field (stored in Hash)
// Returns true if value was removed, false if it didn't exist
func (c *Client) RemoveFromSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hashKey := c.buildHashKey(hashID)

	// Get current value
	current, err := c.client.HGet(ctx, hashKey, property).Result()
	if err == redis.Nil {
		return false, nil // Field doesn't exist
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

	// Save back (or delete field if empty)
	if len(newValues) == 0 {
		return true, c.client.HDel(ctx, hashKey, property).Err()
	}

	newValue := strings.Join(newValues, "|")
	return true, c.client.HSet(ctx, hashKey, property, newValue).Err()
}

// GetMemoryInfo returns Redis memory usage information
func (c *Client) GetMemoryInfo() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := c.client.Info(ctx, "memory").Result()
	if err != nil {
		return "", fmt.Errorf("info memory failed: %w", err)
	}

	// Extract used_memory_human from info string
	for _, line := range strings.Split(info, "\r\n") {
		if strings.HasPrefix(line, "used_memory_human:") {
			return strings.TrimPrefix(line, "used_memory_human:"), nil
		}
	}

	return "N/A", nil
}

// ClearAllMetadata deletes all file metadata and index
// Returns the number of files deleted
func (c *Client) ClearAllMetadata() (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Get all hash IDs from index
	indexKey := c.buildIndexKey()
	hashIDs, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return 0, fmt.Errorf("smembers failed: %w", err)
	}

	var deletedCount int64

	// Delete each file's hash
	for _, hashID := range hashIDs {
		hashKey := c.buildHashKey(hashID)
		if err := c.client.Del(ctx, hashKey).Err(); err != nil {
			log.Printf("[Storage] Warning: failed to delete %s: %v", hashKey, err)
			continue
		}
		deletedCount++
	}

	// Delete the index set
	if err := c.client.Del(ctx, indexKey).Err(); err != nil {
		log.Printf("[Storage] Warning: failed to delete index: %v", err)
	}

	log.Printf("[Storage] Cleared %d file metadata entries", deletedCount)
	return deletedCount, nil
}

// GetRedisClient returns the underlying redis client for advanced operations
func (c *Client) GetRedisClient() *redis.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

// GetPrefix returns the key prefix used by this client
func (c *Client) GetPrefix() string {
	return c.prefix
}
