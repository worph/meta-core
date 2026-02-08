package cache

import (
	"time"
)

// CacheEntry represents a cached file
type CacheEntry struct {
	OriginalPath string    `json:"originalPath"` // Relative to /files/
	CachePath    string    `json:"cachePath"`    // Path in /meta-core/cache/
	Size         int64     `json:"size"`
	CachedAt     time.Time `json:"cachedAt"`
	LastAccessed time.Time `json:"lastAccessed"`
	ContentType  string    `json:"contentType"`
}

// CacheStats provides cache statistics
type CacheStats struct {
	Enabled       bool    `json:"enabled"`
	TotalSize     int64   `json:"totalSize"`     // Current cache size in bytes
	MaxSize       int64   `json:"maxSize"`       // Maximum cache size in bytes
	EntryCount    int     `json:"entryCount"`    // Number of cached entries
	HitCount      int64   `json:"hitCount"`      // Number of cache hits
	MissCount     int64   `json:"missCount"`     // Number of cache misses
	EvictionCount int64   `json:"evictionCount"` // Number of evictions
	HitRate       float64 `json:"hitRate"`       // Hit rate percentage
	TTLSeconds    int     `json:"ttlSeconds"`    // TTL in seconds
}

// CacheIndex is the persistent index structure
type CacheIndex struct {
	Version   int                    `json:"version"`
	Entries   map[string]*CacheEntry `json:"entries"` // keyed by OriginalPath
	Stats     *CacheStats            `json:"stats"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// NewCacheIndex creates a new empty cache index
func NewCacheIndex() *CacheIndex {
	return &CacheIndex{
		Version: 1,
		Entries: make(map[string]*CacheEntry),
		Stats: &CacheStats{
			Enabled: true,
		},
		UpdatedAt: time.Now(),
	}
}
