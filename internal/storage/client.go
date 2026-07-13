package storage

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
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

// buildKeyPrefix constructs the prefix for a file's flat keys
func (c *Client) buildKeyPrefix(hashID string) string {
	return c.buildKey(fmt.Sprintf("file:%s/", hashID))
}

// buildIndexKey constructs the key for the file index set
func (c *Client) buildIndexKey() string {
	return c.buildKey("file:__index__")
}

// GetMetadataFlat retrieves all metadata for a file as a flat map
// Uses flat keys: SCAN file:{hashId}/* then MGET
func (c *Client) GetMetadataFlat(hashID string) (map[string]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKeyPrefix(hashID)
	result := make(map[string]string)

	// Scan for all keys with this prefix
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}

		if len(keys) > 0 {
			values, err := c.client.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, fmt.Errorf("mget failed: %w", err)
			}
			for i, key := range keys {
				field := strings.TrimPrefix(key, prefix)
				if values[i] != nil {
					result[field] = values[i].(string)
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

// SetMetadataFlat stores metadata for a file using flat keys
// Uses pipeline SET for each key: file:{hashId}/{property}
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

	prefix := c.buildKeyPrefix(hashID)
	pipe := c.client.Pipeline()

	for field, value := range metadata {
		pipe.Set(ctx, prefix+field, value, 0)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline exec failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return fmt.Errorf("sadd index failed: %w", err)
	}

	// Auto-register reverse-index aliases for any cids/<cid> key-set members in
	// the payload. That is the ONLY shape recognised — the legacy flat cid_* /
	// midhash256 named fields are not (see cid_resolution.go: cidFromKeysetField).
	c.maybeAddAliasesFromMetadataLocked(ctx, hashID, metadata)

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
// Uses flat key: GET file:{hashId}/{property}
func (c *Client) GetProperty(hashID, property string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKeyPrefix(hashID) + property
	result, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return result, err
}

// SetProperty sets a single property value
// Uses flat key: SET file:{hashId}/{property} {value}
func (c *Client) SetProperty(hashID, property, value string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKeyPrefix(hashID) + property

	// Use SET to set the key
	if err := c.client.Set(ctx, key, value, 0).Err(); err != nil {
		return fmt.Errorf("set failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return fmt.Errorf("sadd index failed: %w", err)
	}

	// Auto-register reverse-index alias if this is a CID field.
	_ = c.maybeAddAliasFromFieldLocked(ctx, hashID, property, value)

	return nil
}

// DeleteMetadata deletes all metadata for a file
// Uses SCAN + DEL for flat keys: file:{hashId}/*
// Also unmaps every reverse-index entry registered for this root, so a
// subsequent GET /api/meta/<old-cid> correctly returns 404 instead of
// pointing at a UUID with no keys.
func (c *Client) DeleteMetadata(hashID string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := c.buildKeyPrefix(hashID)

	// Read the cids key-set first — once file:<uuid>/* is gone we've lost the
	// list of reverse-index keys that need cleanup.
	cids, err := c.cidsForRootLocked(ctx, hashID)
	if err != nil {
		return 0, err
	}
	if len(cids) > 0 {
		idxKeys := make([]string, 0, len(cids))
		for _, cidStr := range cids {
			idxKeys = append(idxKeys, c.buildCIDIndexKey(cidStr))
		}
		if err := c.client.Del(ctx, idxKeys...).Err(); err != nil {
			return 0, fmt.Errorf("del cid index: %w", err)
		}
	}

	// Scan and delete all keys with this prefix
	var deletedCount int64
	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return deletedCount, fmt.Errorf("scan failed: %w", err)
		}

		if len(keys) > 0 {
			deleted, err := c.client.Del(ctx, keys...).Result()
			if err != nil {
				return deletedCount, fmt.Errorf("del failed: %w", err)
			}
			deletedCount += deleted
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	// Remove from index set
	if err := c.client.SRem(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return deletedCount, fmt.Errorf("srem index failed: %w", err)
	}

	return deletedCount, nil
}

// CountFiles returns the number of unique files stored
func (c *Client) CountFiles() (int, error) {
	hashIDs, err := c.GetAllHashIDs()
	if err != nil {
		return 0, err
	}
	return len(hashIDs), nil
}

// LookupPathByCID resolves a bare CID to its file path on disk via the
// reverse index. O(1) lookup followed by a single GET of file:<uuid>/filePath.
//
// Any registered CID resolves — the midhash, a sibling sha2-256, an IPFS
// CID, a btih, etc. (they all alias to the same root). The bare CID is what
// the editor passes to /api/file/{cid}/info and what plugins write (e.g.
// `backdrop=<bare-cid>`).
//
// Plugin output (TMDB posters/backdrops, extracted subtitles, etc.) is
// resolved through this same path: the plugin output directory is a
// watcher root (see config.DefaultWatcherPaths), so every plugin artefact
// gets a midhash alias and resolves through the reverse index like any
// other file. There is no path-companion sidecar field.
func (c *Client) LookupPathByCID(cidStr string) (string, error) {
	uuid, err := c.GetByCID(cidStr)
	if err != nil {
		return "", err
	}
	if uuid == "" {
		return "", nil
	}
	fp, err := c.GetProperty(uuid, "filePath")
	if err != nil {
		return "", err
	}
	return normalizeFilesRelativePath(fp), nil
}

// normalizeFilesRelativePath strips a leading "/files/" prefix so handlers can
// safely filepath.Join(FilesPath, …) without double-prefixing. Different
// plugins store paths inconsistently — poster/backdrop use FilesPath-relative
// ("/plugin/…") while filePath is FilesPath-absolute ("/files/watch/…").
func normalizeFilesRelativePath(p string) string {
	return strings.TrimPrefix(p, "/files")
}

// getMetadataFlatInternal mirrors GetMetadataFlat without re-acquiring the lock.
// Callers must already hold c.mu.
func (c *Client) getMetadataFlatInternal(ctx context.Context, hashID string) (map[string]string, error) {
	prefix := c.buildKeyPrefix(hashID)
	result := make(map[string]string)

	var cursor uint64
	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan failed: %w", err)
		}
		if len(keys) > 0 {
			values, err := c.client.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, fmt.Errorf("mget failed: %w", err)
			}
			for i, key := range keys {
				if values[i] == nil {
					continue
				}
				field := strings.TrimPrefix(key, prefix)
				result[field] = values[i].(string)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return result, nil
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
// Uses pipeline SET for flat keys: file:{hashId}/{property}
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

	prefix := c.buildKeyPrefix(hashID)
	pipe := c.client.Pipeline()

	for field, value := range metadata {
		pipe.Set(ctx, prefix+field, value, 0)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("pipeline exec failed: %w", err)
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return len(metadata), fmt.Errorf("sadd index failed: %w", err)
	}

	// Auto-register reverse-index aliases for any cids/<cid> key-set members in
	// the payload. That is the ONLY shape recognised — the legacy flat cid_* /
	// midhash256 named fields are not (see cid_resolution.go: cidFromKeysetField).
	c.maybeAddAliasesFromMetadataLocked(ctx, hashID, metadata)

	return len(metadata), nil
}

// DeleteProperty deletes a single property
// Uses flat key: DEL file:{hashId}/{property}
func (c *Client) DeleteProperty(hashID, property string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKeyPrefix(hashID) + property
	return c.client.Del(ctx, key).Err()
}

// AddToSet adds a value to a set-type field (stored as pipe-delimited string in flat key)
// Returns true if value was added, false if it already existed
func (c *Client) AddToSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKeyPrefix(hashID) + property

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

	// Save back using SET
	if err := c.client.Set(ctx, key, newValue, 0).Err(); err != nil {
		return false, err
	}

	// Add to index set
	if err := c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err(); err != nil {
		return true, fmt.Errorf("sadd index failed: %w", err)
	}

	return true, nil
}

// RemoveFromSet removes a value from a set-type field (stored in flat key)
// Returns true if value was removed, false if it didn't exist
func (c *Client) RemoveFromSet(hashID, property, value string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	key := c.buildKeyPrefix(hashID) + property

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

	// Save back (or delete key if empty)
	if len(newValues) == 0 {
		return true, c.client.Del(ctx, key).Err()
	}

	newValue := strings.Join(newValues, "|")
	return true, c.client.Set(ctx, key, newValue, 0).Err()
}

// FileTuple holds the (uuid, path, size, mtime, midhash) record for one
// file. Used by the watcher hydrator at startup to avoid re-mid-hashing
// files whose tuple matches what's already in Redis.
//
// HashID is the root identifier (UUIDv7 in the current model). Hydration
// uses HashID + (FilePath, Size, MtimeNano) to seed the in-memory state so
// the next scan can skip re-mid-hashing files whose size+mtime are unchanged.
type FileTuple struct {
	HashID    string
	FilePath  string
	Size      int64
	MtimeNano int64
}

// GetTuplesForAllFiles returns one FileTuple per indexed root, fetched in
// batched MGETs (no SCAN per root). Skips files missing any of filePath,
// sizeByte, or mtimeNano. Sub-second on local Redis for ~10K files.
//
// The midhash is intentionally NOT fetched here: it now lives only as a
// cids/<cid> key-set member (no named field), and the skip-rehash decision
// is purely size+mtime — so reading it would cost a SCAN per file for no
// benefit.
func (c *Client) GetTuplesForAllFiles() ([]FileTuple, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	hashIDs, err := c.client.SMembers(ctx, c.buildIndexKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers index: %w", err)
	}
	if len(hashIDs) == 0 {
		return nil, nil
	}

	// Build the key list: 4 keys per root, in a fixed order so we can decode
	// the MGET response by position. fields[i] for hashIDs[j] sits at
	// keys[j*4+i].
	const fieldsPerFile = 3
	fields := [fieldsPerFile]string{"filePath", "sizeByte", "mtimeNano"}
	keys := make([]string, 0, len(hashIDs)*fieldsPerFile)
	for _, h := range hashIDs {
		prefix := c.buildKeyPrefix(h)
		for _, f := range fields {
			keys = append(keys, prefix+f)
		}
	}

	// Chunked MGET — Redis caps args per command; 1000 is a safe batch.
	const batch = 1000
	values := make([]interface{}, 0, len(keys))
	for start := 0; start < len(keys); start += batch {
		end := start + batch
		if end > len(keys) {
			end = len(keys)
		}
		chunk, err := c.client.MGet(ctx, keys[start:end]...).Result()
		if err != nil {
			return nil, fmt.Errorf("mget chunk [%d:%d]: %w", start, end, err)
		}
		values = append(values, chunk...)
	}

	out := make([]FileTuple, 0, len(hashIDs))
	for i, h := range hashIDs {
		base := i * fieldsPerFile
		filePath, _ := values[base].(string)
		sizeStr, _ := values[base+1].(string)
		mtimeStr, _ := values[base+2].(string)
		if filePath == "" || sizeStr == "" || mtimeStr == "" {
			continue
		}
		size, err := strconv.ParseInt(sizeStr, 10, 64)
		if err != nil {
			continue
		}
		mtime, err := strconv.ParseInt(mtimeStr, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, FileTuple{
			HashID:    h,
			FilePath:  filePath,
			Size:      size,
			MtimeNano: mtime,
		})
	}
	return out, nil
}

// SearchTuple is one record's search state: its hashID, a lowercased haystack of
// its searchable text fields (for token/substring match), and the full flat
// metadata map (so a search hit is returned straight from memory — no per-result
// Redis fetch).
type SearchTuple struct {
	HashID   string
	Haystack string
	Fields   map[string]string
}

// searchIndexFields are the metadata fields folded into a record's search
// haystack — must stay in sync with the fields the search handler documents.
var searchIndexFields = []string{"title", "fileName", "originaltitle", "showtitle", "filePath"}

// GetSearchIndexTuples reads EVERY record's full metadata in a single keyspace
// SCAN pass over the flat `file:{hashID}/{field}` keys plus batched MGETs,
// grouped by record — one bulk pass instead of a SCAN+MGET per record (which is
// O(records) Redis round-trips, ~40s over 72k field keys). It is the bulk source
// the API folds into its in-memory search index, so per-request search does zero
// Redis round-trips (match AND result metadata both come from memory).
func (c *Client) GetSearchIndexTuples() ([]SearchTuple, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	filePrefix := c.buildKey("file:") // key layout: {filePrefix}{hashID}/{field}
	indexKey := c.buildIndexKey()     // {filePrefix}__index__ (a SET, skip it)
	pattern := filePrefix + "*"

	records := make(map[string]map[string]string)
	keyBatch := make([]string, 0, 1000)

	flush := func() error {
		if len(keyBatch) == 0 {
			return nil
		}
		values, err := c.client.MGet(ctx, keyBatch...).Result()
		if err != nil {
			return fmt.Errorf("mget: %w", err)
		}
		for i, k := range keyBatch {
			v, ok := values[i].(string)
			if !ok {
				continue
			}
			// {filePrefix}{hashID}/{field}; hashID has no '/', field may.
			rest := strings.TrimPrefix(k, filePrefix)
			slash := strings.IndexByte(rest, '/')
			if slash < 0 {
				continue
			}
			hashID, field := rest[:slash], rest[slash+1:]
			m := records[hashID]
			if m == nil {
				m = make(map[string]string)
				records[hashID] = m
			}
			m[field] = v
		}
		keyBatch = keyBatch[:0]
		return nil
	}

	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		for _, k := range keys {
			if k == indexKey {
				continue
			}
			keyBatch = append(keyBatch, k)
			if len(keyBatch) >= 1000 {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}

	out := make([]SearchTuple, 0, len(records))
	for hashID, fields := range records {
		var sb strings.Builder
		for _, f := range searchIndexFields {
			if v, ok := fields[f]; ok && v != "" {
				sb.WriteString(strings.ToLower(v))
				sb.WriteByte('\n')
			}
		}
		out = append(out, SearchTuple{HashID: hashID, Haystack: sb.String(), Fields: fields})
	}
	return out, nil
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

	// Delete each file's flat keys
	for _, hashID := range hashIDs {
		prefix := c.buildKeyPrefix(hashID)

		// Scan and delete all keys with this prefix
		var cursor uint64
		for {
			keys, nextCursor, err := c.client.Scan(ctx, cursor, prefix+"*", 1000).Result()
			if err != nil {
				log.Printf("[Storage] Warning: failed to scan %s: %v", prefix, err)
				break
			}

			if len(keys) > 0 {
				if err := c.client.Del(ctx, keys...).Err(); err != nil {
					log.Printf("[Storage] Warning: failed to delete keys for %s: %v", hashID, err)
				}
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
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

// ScanKeys lists the immediate children at the given prefix, S3-style.
//
// Walks the keyspace under prefix using Redis SCAN. When delimiter is set,
// keys with a delimiter occurrence after the prefix collapse into a branch
// path (the prefix up to and including that first delimiter); the rest are
// returned as leaves. When delimiter is empty all matching keys are leaves.
//
// Continues SCAN-ing until either the keyspace is exhausted (nextCursor == "0")
// or leaves+branches reaches maxResults — whichever comes first. The caller
// passes nextCursor back to continue. Results are deduped and sorted.
func (c *Client) ScanKeys(prefix, delimiter, cursorStr string, maxResults int) ([]string, []string, string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, nil, "", fmt.Errorf("not connected")
	}

	if maxResults <= 0 {
		maxResults = 500
	}

	var cursor uint64
	if cursorStr != "" {
		if v, err := strconv.ParseUint(cursorStr, 10, 64); err == nil {
			cursor = v
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fullPrefix := c.buildKey(prefix)
	pattern := escapeGlob(fullPrefix) + "*"

	leafSet := make(map[string]struct{})
	branchSet := make(map[string]struct{})

	for {
		// SCAN COUNT is a hint; ask for a larger batch when grouping by
		// delimiter since many keys collapse into one branch.
		scanCount := int64(maxResults * 4)
		if scanCount > 5000 {
			scanCount = 5000
		}
		keys, next, err := c.client.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			return nil, nil, "", fmt.Errorf("scan failed: %w", err)
		}
		for _, k := range keys {
			userKey := strings.TrimPrefix(k, c.prefix)
			suffix := strings.TrimPrefix(userKey, prefix)
			if delimiter != "" {
				if idx := strings.Index(suffix, delimiter); idx >= 0 {
					branchSet[prefix+suffix[:idx+len(delimiter)]] = struct{}{}
					continue
				}
			}
			leafSet[userKey] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
		if len(leafSet)+len(branchSet) >= maxResults {
			break
		}
	}

	leaves := make([]string, 0, len(leafSet))
	for k := range leafSet {
		leaves = append(leaves, k)
	}
	branches := make([]string, 0, len(branchSet))
	for k := range branchSet {
		branches = append(branches, k)
	}
	sort.Strings(leaves)
	sort.Strings(branches)
	return leaves, branches, strconv.FormatUint(cursor, 10), nil
}

// FindMatch is a single value-search hit.
type FindMatch struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	EntryPath string `json:"entryPath"`
	Field     string `json:"field"`
}

// FindByValue scans every key whose path ends with /<field> for each field
// in fields, MGETs them, and returns the keys whose value contains the
// substring (case-insensitive). Returns truncated when limit was hit before
// the scan exhausted.
//
// EntryPath is the parent prefix up to (and including) the slash before
// field — i.e. the file branch path. Useful for revealing the matched entry
// in the tree.
func (c *Client) FindByValue(contains string, fields []string, limit int) ([]FindMatch, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, false, fmt.Errorf("not connected")
	}
	if contains == "" || len(fields) == 0 {
		return nil, false, nil
	}
	if limit <= 0 {
		limit = 200
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	needle := strings.ToLower(contains)
	var matches []FindMatch
	truncated := false

	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if len(matches) >= limit {
			truncated = true
			break
		}
		// Walk all keys ending with /field via SCAN MATCH "*/<field>".
		pattern := "*/" + escapeGlob(field)
		if c.prefix != "" {
			pattern = escapeGlob(c.prefix) + pattern
		}
		var keysToFetch []string
		var cursor uint64
		for {
			keys, next, err := c.client.Scan(ctx, cursor, pattern, 1000).Result()
			if err != nil {
				return nil, false, fmt.Errorf("scan failed: %w", err)
			}
			keysToFetch = append(keysToFetch, keys...)
			cursor = next
			if cursor == 0 {
				break
			}
			if len(keysToFetch) > 20000 {
				break
			}
		}
		// MGET in chunks (Redis limits the number of args per command).
		for chunkStart := 0; chunkStart < len(keysToFetch); chunkStart += 500 {
			chunkEnd := chunkStart + 500
			if chunkEnd > len(keysToFetch) {
				chunkEnd = len(keysToFetch)
			}
			chunk := keysToFetch[chunkStart:chunkEnd]
			values, err := c.client.MGet(ctx, chunk...).Result()
			if err != nil {
				return nil, false, fmt.Errorf("mget failed: %w", err)
			}
			for i, v := range values {
				if v == nil {
					continue
				}
				sv, _ := v.(string)
				if !strings.Contains(strings.ToLower(sv), needle) {
					continue
				}
				userKey := strings.TrimPrefix(chunk[i], c.prefix)
				entryPath := strings.TrimSuffix(userKey, field)
				matches = append(matches, FindMatch{
					Key:       userKey,
					Value:     sv,
					EntryPath: entryPath,
					Field:     field,
				})
				if len(matches) >= limit {
					truncated = true
					break
				}
			}
			if truncated {
				break
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].Key < matches[j].Key })
	return matches, truncated, nil
}

// SearchKeys returns up to limit keys whose full path contains the substring.
// truncated is true when the search stopped at limit before exhausting Redis.
func (c *Client) SearchKeys(contains string, limit int) ([]string, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, false, fmt.Errorf("not connected")
	}

	if contains == "" {
		return nil, false, nil
	}
	if limit <= 0 {
		limit = 500
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pattern := escapeGlob(c.prefix) + "*" + escapeGlob(contains) + "*"

	results := make([]string, 0, limit)
	truncated := false
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 1000).Result()
		if err != nil {
			return nil, false, fmt.Errorf("scan failed: %w", err)
		}
		for _, k := range keys {
			if len(results) >= limit {
				truncated = true
				break
			}
			results = append(results, strings.TrimPrefix(k, c.prefix))
		}
		cursor = next
		if truncated || cursor == 0 {
			break
		}
	}
	sort.Strings(results)
	return results, truncated, nil
}

// GetKeyInfo returns the Redis type ("string", "set", "hash", ...) and, for
// string-typed keys, the value. exists is false when the key is absent.
func (c *Client) GetKeyInfo(key string) (kind string, value string, exists bool, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", "", false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := c.buildKey(key)
	t, err := c.client.Type(ctx, full).Result()
	if err != nil {
		return "", "", false, fmt.Errorf("type failed: %w", err)
	}
	if t == "none" {
		return "", "", false, nil
	}
	if t != "string" {
		return t, "", true, nil
	}
	v, err := c.client.Get(ctx, full).Result()
	if err == redis.Nil {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("get failed: %w", err)
	}
	return "string", v, true, nil
}

// SetRawValue writes a string value at the exact key. Refuses to overwrite a
// key whose existing Redis type is not "string".
func (c *Client) SetRawValue(key, value string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	full := c.buildKey(key)
	t, err := c.client.Type(ctx, full).Result()
	if err != nil {
		return fmt.Errorf("type failed: %w", err)
	}
	if t != "none" && t != "string" {
		return fmt.Errorf("key has incompatible type %q (only string is editable)", t)
	}
	return c.client.Set(ctx, full, value, 0).Err()
}

// DeleteRawKey deletes the exact key. Returns true if the key existed.
func (c *Client) DeleteRawKey(key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	n, err := c.client.Del(ctx, c.buildKey(key)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// IndexAdd adds a hash ID to the file index set. Idempotent.
func (c *Client) IndexAdd(hashID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.client.SAdd(ctx, c.buildIndexKey(), hashID).Err()
}

// IndexRemoveIfEmpty removes hashID from the index set when no
// file:<hashID>/* keys remain. Returns true if the index entry was removed.
func (c *Client) IndexRemoveIfEmpty(hashID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return false, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	prefix := c.buildKeyPrefix(hashID)
	keys, _, err := c.client.Scan(ctx, 0, prefix+"*", 1).Result()
	if err != nil {
		return false, fmt.Errorf("scan failed: %w", err)
	}
	if len(keys) > 0 {
		return false, nil
	}
	removed, err := c.client.SRem(ctx, c.buildIndexKey(), hashID).Result()
	if err != nil {
		return false, fmt.Errorf("srem failed: %w", err)
	}
	return removed > 0, nil
}

// escapeGlob escapes Redis SCAN MATCH glob metacharacters so a literal string
// can be embedded inside a pattern. Redis uses *, ?, [, \ as metacharacters.
func escapeGlob(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
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

// XAdd publishes an event to a Redis Stream
// If maxLen > 0, uses XADD with MAXLEN ~ for approximate trimming
// If maxLen <= 0, no trimming is applied (stream grows unbounded)
func (c *Client) XAdd(stream string, maxLen int64, fields map[string]interface{}) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Build XADD arguments
	args := &redis.XAddArgs{
		Stream: stream,
		Values: fields,
	}

	// Only set MaxLen if positive (0 means no limit)
	if maxLen > 0 {
		args.MaxLen = maxLen
		args.Approx = true
	}

	id, err := c.client.XAdd(ctx, args).Result()
	if err != nil {
		return "", fmt.Errorf("xadd failed: %w", err)
	}

	return id, nil
}

// ClearStream deletes a Redis stream entirely
func (c *Client) ClearStream(stream string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.client.Del(ctx, stream).Err(); err != nil {
		return fmt.Errorf("del stream failed: %w", err)
	}

	return nil
}

// StreamEntry represents one entry from a Redis stream.
type StreamEntry struct {
	ID     string
	Values map[string]interface{}
}

// XRevRange reads stream entries from newest to oldest.
// start defaults to "+" (newest) and stop defaults to "-" (oldest) when empty.
// If count > 0, at most that many entries are returned.
func (c *Client) XRevRange(stream, start, stop string, count int64) ([]StreamEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	if start == "" {
		start = "+"
	}
	if stop == "" {
		stop = "-"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var msgs []redis.XMessage
	var err error
	if count > 0 {
		msgs, err = c.client.XRevRangeN(ctx, stream, start, stop, count).Result()
	} else {
		msgs, err = c.client.XRevRange(ctx, stream, start, stop).Result()
	}
	if err != nil {
		return nil, fmt.Errorf("xrevrange failed: %w", err)
	}

	entries := make([]StreamEntry, 0, len(msgs))
	for _, m := range msgs {
		entries = append(entries, StreamEntry{ID: m.ID, Values: m.Values})
	}
	return entries, nil
}

// GetStreamLength returns the length of a Redis stream
func (c *Client) GetStreamLength(stream string) (int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return 0, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	length, err := c.client.XLen(ctx, stream).Result()
	if err != nil {
		return 0, fmt.Errorf("xlen failed: %w", err)
	}

	return length, nil
}
