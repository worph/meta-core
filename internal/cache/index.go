package cache

import (
	"container/list"
	"sync"
	"time"
)

// LRUIndex maintains an in-memory LRU index of cache entries
type LRUIndex struct {
	mu       sync.RWMutex
	entries  map[string]*list.Element // path -> list element
	lruList  *list.List               // LRU order (front = most recent)
	maxSize  int64                    // Maximum total size in bytes
	currSize int64                    // Current total size in bytes

	// Stats
	hitCount      int64
	missCount     int64
	evictionCount int64
}

// lruEntry wraps CacheEntry for the linked list
type lruEntry struct {
	entry *CacheEntry
}

// NewLRUIndex creates a new LRU index
func NewLRUIndex(maxSizeGB int) *LRUIndex {
	return &LRUIndex{
		entries: make(map[string]*list.Element),
		lruList: list.New(),
		maxSize: int64(maxSizeGB) * 1024 * 1024 * 1024, // GB to bytes
	}
}

// Get retrieves a cache entry and updates its LRU position
// Returns nil if not found
func (idx *LRUIndex) Get(path string) *CacheEntry {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	elem, exists := idx.entries[path]
	if !exists {
		idx.missCount++
		return nil
	}

	// Move to front (most recently used)
	idx.lruList.MoveToFront(elem)

	// Update last accessed time
	entry := elem.Value.(*lruEntry).entry
	entry.LastAccessed = time.Now()

	idx.hitCount++
	return entry
}

// Put adds or updates a cache entry
func (idx *LRUIndex) Put(entry *CacheEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if elem, exists := idx.entries[entry.OriginalPath]; exists {
		// Update existing entry
		oldEntry := elem.Value.(*lruEntry).entry
		idx.currSize -= oldEntry.Size
		idx.currSize += entry.Size

		elem.Value.(*lruEntry).entry = entry
		idx.lruList.MoveToFront(elem)
	} else {
		// Add new entry
		le := &lruEntry{entry: entry}
		elem := idx.lruList.PushFront(le)
		idx.entries[entry.OriginalPath] = elem
		idx.currSize += entry.Size
	}
}

// Remove removes a cache entry by path
// Returns the removed entry or nil if not found
func (idx *LRUIndex) Remove(path string) *CacheEntry {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	elem, exists := idx.entries[path]
	if !exists {
		return nil
	}

	entry := elem.Value.(*lruEntry).entry
	idx.lruList.Remove(elem)
	delete(idx.entries, path)
	idx.currSize -= entry.Size

	return entry
}

// GetLRUEntry returns the least recently used entry without removing it
// Returns nil if the index is empty
func (idx *LRUIndex) GetLRUEntry() *CacheEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	back := idx.lruList.Back()
	if back == nil {
		return nil
	}

	return back.Value.(*lruEntry).entry
}

// PopLRUEntry removes and returns the least recently used entry
// Returns nil if the index is empty
func (idx *LRUIndex) PopLRUEntry() *CacheEntry {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	back := idx.lruList.Back()
	if back == nil {
		return nil
	}

	entry := back.Value.(*lruEntry).entry
	idx.lruList.Remove(back)
	delete(idx.entries, entry.OriginalPath)
	idx.currSize -= entry.Size
	idx.evictionCount++

	return entry
}

// GetExpiredEntries returns all entries that have exceeded the TTL
func (idx *LRUIndex) GetExpiredEntries(ttl time.Duration) []*CacheEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var expired []*CacheEntry
	cutoff := time.Now().Add(-ttl)

	for elem := idx.lruList.Back(); elem != nil; elem = elem.Prev() {
		entry := elem.Value.(*lruEntry).entry
		if entry.CachedAt.Before(cutoff) {
			expired = append(expired, entry)
		}
	}

	return expired
}

// Size returns the current total size of cached files
func (idx *LRUIndex) Size() int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.currSize
}

// Count returns the number of entries in the index
func (idx *LRUIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.entries)
}

// MaxSize returns the maximum cache size
func (idx *LRUIndex) MaxSize() int64 {
	return idx.maxSize
}

// NeedsEviction returns true if the cache size exceeds the maximum
func (idx *LRUIndex) NeedsEviction() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.currSize > idx.maxSize
}

// Stats returns cache statistics
func (idx *LRUIndex) Stats() *CacheStats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	total := idx.hitCount + idx.missCount
	var hitRate float64
	if total > 0 {
		hitRate = float64(idx.hitCount) / float64(total) * 100
	}

	return &CacheStats{
		Enabled:       true,
		TotalSize:     idx.currSize,
		MaxSize:       idx.maxSize,
		EntryCount:    len(idx.entries),
		HitCount:      idx.hitCount,
		MissCount:     idx.missCount,
		EvictionCount: idx.evictionCount,
		HitRate:       hitRate,
	}
}

// GetAllEntries returns all cache entries (for persistence)
func (idx *LRUIndex) GetAllEntries() map[string]*CacheEntry {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	result := make(map[string]*CacheEntry, len(idx.entries))
	for path, elem := range idx.entries {
		result[path] = elem.Value.(*lruEntry).entry
	}
	return result
}

// LoadFromMap loads entries from a map (for loading persisted index)
func (idx *LRUIndex) LoadFromMap(entries map[string]*CacheEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Clear existing entries
	idx.lruList.Init()
	idx.entries = make(map[string]*list.Element)
	idx.currSize = 0

	// Load entries sorted by LastAccessed (oldest first for proper LRU order)
	// For simplicity, we just add them in arbitrary order - the access pattern will sort them
	for _, entry := range entries {
		le := &lruEntry{entry: entry}
		elem := idx.lruList.PushBack(le) // Add to back (oldest)
		idx.entries[entry.OriginalPath] = elem
		idx.currSize += entry.Size
	}
}

// Clear removes all entries from the index
func (idx *LRUIndex) Clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.lruList.Init()
	idx.entries = make(map[string]*list.Element)
	idx.currSize = 0
}
