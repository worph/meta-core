package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metazla/meta-core/internal/config"
)

// Manager handles caching operations for WebDAV files
type Manager struct {
	config     *config.Config
	index      *LRUIndex
	filesPath  string
	cachePath  string
	ttl        time.Duration
	enabled    bool

	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	cleanupWg  sync.WaitGroup
}

// NewManager creates a new cache manager
func NewManager(cfg *config.Config) (*Manager, error) {
	if !cfg.CacheEnabled {
		log.Println("[Cache] Caching is disabled")
		return &Manager{
			config:  cfg,
			enabled: false,
		}, nil
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(cfg.CacheDir(), 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m := &Manager{
		config:    cfg,
		index:     NewLRUIndex(cfg.CacheMaxSizeGB),
		filesPath: cfg.FilesPath,
		cachePath: cfg.CacheDir(),
		ttl:       time.Duration(cfg.CacheTTLSeconds) * time.Second,
		enabled:   true,
		ctx:       ctx,
		cancel:    cancel,
	}

	// Load existing index from disk
	if err := m.loadIndex(); err != nil {
		log.Printf("[Cache] Warning: failed to load index: %v (starting fresh)", err)
	}

	return m, nil
}

// Start starts the cache manager (cleanup goroutine)
func (m *Manager) Start() error {
	if !m.enabled {
		return nil
	}

	log.Printf("[Cache] Starting cache manager (max: %dGB, ttl: %v)",
		m.config.CacheMaxSizeGB, m.ttl)

	// Start cleanup goroutine
	m.cleanupWg.Add(1)
	go m.cleanupLoop()

	return nil
}

// Stop stops the cache manager
func (m *Manager) Stop() error {
	if !m.enabled {
		return nil
	}

	log.Println("[Cache] Stopping cache manager...")
	m.cancel()
	m.cleanupWg.Wait()

	// Persist index
	if err := m.saveIndex(); err != nil {
		log.Printf("[Cache] Warning: failed to save index: %v", err)
	}

	return nil
}

// Enabled returns whether caching is enabled
func (m *Manager) Enabled() bool {
	return m.enabled
}

// Get retrieves a file from cache or fetches it from origin
// Returns the cached file path and content type, or an error
func (m *Manager) Get(path string) (string, string, error) {
	if !m.enabled {
		// Return original path if caching disabled
		return filepath.Join(m.filesPath, path), "", nil
	}

	// Normalize path (remove leading slash if present)
	path = strings.TrimPrefix(path, "/")

	// Check cache
	entry := m.index.Get(path)
	if entry != nil {
		// Verify cached file still exists
		if _, err := os.Stat(entry.CachePath); err == nil {
			log.Printf("[Cache] HIT: %s", path)
			return entry.CachePath, entry.ContentType, nil
		}
		// Cached file missing, remove from index
		m.index.Remove(path)
	}

	log.Printf("[Cache] MISS: %s", path)

	// Fetch from origin and cache
	cachedPath, contentType, err := m.fetchAndCache(path)
	if err != nil {
		return "", "", err
	}

	return cachedPath, contentType, nil
}

// GetOriginalPath returns the original file path (for when caching is bypassed)
func (m *Manager) GetOriginalPath(path string) string {
	path = strings.TrimPrefix(path, "/")
	return filepath.Join(m.filesPath, path)
}

// fetchAndCache fetches a file from origin and caches it
func (m *Manager) fetchAndCache(path string) (string, string, error) {
	originPath := filepath.Join(m.filesPath, path)

	// Check if origin file exists
	info, err := os.Stat(originPath)
	if err != nil {
		return "", "", err
	}

	// Skip caching directories
	if info.IsDir() {
		return originPath, "", nil
	}

	// Generate cache path using SHA256 of original path with 2-char sharding
	cachePath := m.getCachePath(path)

	// Ensure cache subdirectory exists
	cacheDir := filepath.Dir(cachePath)
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", "", fmt.Errorf("failed to create cache dir: %w", err)
	}

	// Copy file to cache
	src, err := os.Open(originPath)
	if err != nil {
		return "", "", err
	}
	defer src.Close()

	dst, err := os.Create(cachePath)
	if err != nil {
		return "", "", err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	if err != nil {
		os.Remove(cachePath)
		return "", "", err
	}

	// Detect content type
	contentType := detectContentType(originPath)

	// Create cache entry
	entry := &CacheEntry{
		OriginalPath: path,
		CachePath:    cachePath,
		Size:         info.Size(),
		CachedAt:     time.Now(),
		LastAccessed: time.Now(),
		ContentType:  contentType,
	}

	// Add to index
	m.index.Put(entry)

	// Trigger eviction if needed
	m.evictIfNeeded()

	log.Printf("[Cache] Cached: %s (%d bytes)", path, info.Size())

	return cachePath, contentType, nil
}

// getCachePath generates a cache path using SHA256 with 2-char directory sharding
func (m *Manager) getCachePath(path string) string {
	hash := sha256.Sum256([]byte(path))
	hexHash := hex.EncodeToString(hash[:])

	// Use first 2 characters for sharding
	shard := hexHash[:2]
	filename := hexHash[2:]

	return filepath.Join(m.cachePath, shard, filename)
}

// Invalidate removes a specific path from the cache
func (m *Manager) Invalidate(path string) error {
	if !m.enabled {
		return nil
	}

	path = strings.TrimPrefix(path, "/")

	entry := m.index.Remove(path)
	if entry == nil {
		return nil // Not in cache
	}

	// Remove cached file
	if err := os.Remove(entry.CachePath); err != nil && !os.IsNotExist(err) {
		log.Printf("[Cache] Warning: failed to remove cached file: %v", err)
	}

	log.Printf("[Cache] Invalidated: %s", path)
	return nil
}

// Clear removes all entries from the cache
func (m *Manager) Clear() error {
	if !m.enabled {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get all entries before clearing
	entries := m.index.GetAllEntries()

	// Clear index
	m.index.Clear()

	// Remove all cached files
	for _, entry := range entries {
		if err := os.Remove(entry.CachePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[Cache] Warning: failed to remove cached file: %v", err)
		}
	}

	// Remove empty shard directories
	files, err := os.ReadDir(m.cachePath)
	if err == nil {
		for _, f := range files {
			if f.IsDir() && len(f.Name()) == 2 {
				dirPath := filepath.Join(m.cachePath, f.Name())
				os.Remove(dirPath) // Only removes if empty
			}
		}
	}

	log.Printf("[Cache] Cleared %d entries", len(entries))
	return nil
}

// Stats returns cache statistics
func (m *Manager) Stats() *CacheStats {
	if !m.enabled {
		return &CacheStats{Enabled: false}
	}

	stats := m.index.Stats()
	stats.TTLSeconds = m.config.CacheTTLSeconds
	return stats
}

// evictIfNeeded evicts LRU entries until cache is under max size
func (m *Manager) evictIfNeeded() {
	for m.index.NeedsEviction() {
		entry := m.index.PopLRUEntry()
		if entry == nil {
			break
		}

		// Remove cached file
		if err := os.Remove(entry.CachePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[Cache] Warning: failed to remove evicted file: %v", err)
		}

		log.Printf("[Cache] Evicted (LRU): %s", entry.OriginalPath)
	}
}

// cleanupLoop periodically cleans up expired entries
func (m *Manager) cleanupLoop() {
	defer m.cleanupWg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.cleanupExpired()
			m.evictIfNeeded()

			// Periodically save index
			if err := m.saveIndex(); err != nil {
				log.Printf("[Cache] Warning: failed to save index: %v", err)
			}
		}
	}
}

// cleanupExpired removes entries that have exceeded TTL
func (m *Manager) cleanupExpired() {
	expired := m.index.GetExpiredEntries(m.ttl)

	for _, entry := range expired {
		m.index.Remove(entry.OriginalPath)

		if err := os.Remove(entry.CachePath); err != nil && !os.IsNotExist(err) {
			log.Printf("[Cache] Warning: failed to remove expired file: %v", err)
		}

		log.Printf("[Cache] Expired (TTL): %s", entry.OriginalPath)
	}

	if len(expired) > 0 {
		log.Printf("[Cache] Cleaned up %d expired entries", len(expired))
	}
}

// loadIndex loads the cache index from disk
func (m *Manager) loadIndex() error {
	indexPath := m.config.CacheIndexPath()

	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No index file yet
		}
		return err
	}

	var idx CacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}

	// Validate and load entries
	validEntries := make(map[string]*CacheEntry)
	for path, entry := range idx.Entries {
		// Verify cached file still exists
		if _, err := os.Stat(entry.CachePath); err == nil {
			validEntries[path] = entry
		}
	}

	m.index.LoadFromMap(validEntries)

	log.Printf("[Cache] Loaded %d entries from index", len(validEntries))
	return nil
}

// saveIndex persists the cache index to disk
func (m *Manager) saveIndex() error {
	indexPath := m.config.CacheIndexPath()

	idx := &CacheIndex{
		Version:   1,
		Entries:   m.index.GetAllEntries(),
		Stats:     m.index.Stats(),
		UpdatedAt: time.Now(),
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically using temp file
	tmpPath := indexPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, indexPath)
}

// detectContentType detects the content type based on file extension
func detectContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	// Common media types
	switch ext {
	case ".mp4":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".srt":
		return "text/plain"
	case ".vtt":
		return "text/vtt"
	case ".ass", ".ssa":
		return "text/x-ssa"
	case ".nfo":
		return "text/plain"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	default:
		return "application/octet-stream"
	}
}

// ServeFile serves a file through the cache, handling HTTP response
func (m *Manager) ServeFile(w http.ResponseWriter, r *http.Request, path string) error {
	cachedPath, contentType, err := m.Get(path)
	if err != nil {
		return err
	}

	// Open the file (cached or original)
	file, err := os.Open(cachedPath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}

	// Set content type if detected
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	// Use http.ServeContent for proper range support
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), file)
	return nil
}
