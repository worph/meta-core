package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/metazla/meta-core/internal/config"
)

const (
	// PartialHashSize is the number of bytes to hash for file identification
	PartialHashSize = 64 * 1024 // 64KB
)

// Watcher monitors directories for file changes
type Watcher struct {
	config     *config.Config
	fsWatcher  *fsnotify.Watcher
	debouncer  *Debouncer
	dispatcher *Dispatcher
	filesPath  string
	watchPaths []string

	mu          sync.RWMutex
	isRunning   bool
	isScanning  bool
	lastScan    int64
	fileCount   int
	stopChan    chan struct{}
	eventBuffer []FileEvent
}

// NewWatcher creates a new file watcher
func NewWatcher(cfg *config.Config, dispatcher *Dispatcher) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	debouncer := NewDebouncer(time.Duration(cfg.DebounceMS) * time.Millisecond)

	w := &Watcher{
		config:      cfg,
		fsWatcher:   fsWatcher,
		debouncer:   debouncer,
		dispatcher:  dispatcher,
		filesPath:   cfg.FilesPath,
		watchPaths:  cfg.WatchFolderList,
		stopChan:    make(chan struct{}),
		eventBuffer: make([]FileEvent, 0, 1000),
	}

	// Set debouncer callback
	debouncer.SetCallback(func(event FileEvent) {
		w.handleDebouncedEvent(event)
	})

	return w, nil
}

// Start begins watching for file changes
func (w *Watcher) Start() error {
	w.mu.Lock()
	if w.isRunning {
		w.mu.Unlock()
		return nil
	}
	w.isRunning = true
	w.mu.Unlock()

	// Add watch paths
	for _, watchPath := range w.watchPaths {
		if err := w.addWatchRecursive(watchPath); err != nil {
			log.Printf("[Watcher] Warning: failed to watch %s: %v", watchPath, err)
		}
	}

	// Start event processing goroutine
	go w.processEvents()

	// Start initial scan
	go w.RunScan()

	log.Printf("[Watcher] Started watching %d paths", len(w.watchPaths))
	return nil
}

// Stop stops the watcher
func (w *Watcher) Stop() error {
	w.mu.Lock()
	if !w.isRunning {
		w.mu.Unlock()
		return nil
	}
	w.isRunning = false
	w.mu.Unlock()

	close(w.stopChan)
	w.debouncer.Stop()
	return w.fsWatcher.Close()
}

// addWatchRecursive adds a directory and all subdirectories to the watch list
func (w *Watcher) addWatchRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}
		if info.IsDir() {
			if err := w.fsWatcher.Add(path); err != nil {
				log.Printf("[Watcher] Warning: cannot watch %s: %v", path, err)
			}
		}
		return nil
	})
}

// processEvents handles fsnotify events
func (w *Watcher) processEvents() {
	for {
		select {
		case <-w.stopChan:
			return

		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			w.handleFsEvent(event)

		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			log.Printf("[Watcher] Error: %v", err)
		}
	}
}

// handleFsEvent converts an fsnotify event to a FileEvent
func (w *Watcher) handleFsEvent(event fsnotify.Event) {
	// Get relative path
	relPath, err := filepath.Rel(w.filesPath, event.Name)
	if err != nil {
		relPath = event.Name
	}

	// Clean up path
	relPath = filepath.ToSlash(relPath)

	// Determine event type
	var eventType FileEventType
	switch {
	case event.Op&fsnotify.Create == fsnotify.Create:
		eventType = EventTypeAdd
		// If it's a new directory, add it to watch
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			w.fsWatcher.Add(event.Name)
		}
	case event.Op&fsnotify.Write == fsnotify.Write:
		eventType = EventTypeChange
	case event.Op&fsnotify.Remove == fsnotify.Remove:
		eventType = EventTypeDelete
	case event.Op&fsnotify.Rename == fsnotify.Rename:
		eventType = EventTypeRename
	default:
		return // Ignore other events
	}

	// Skip directories for file events (except remove)
	if eventType != EventTypeDelete {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			return
		}
	}

	// Get file info if available
	var size int64
	if eventType != EventTypeDelete {
		if info, err := os.Stat(event.Name); err == nil {
			size = info.Size()
		}
	}

	fileEvent := FileEvent{
		Type:      eventType,
		Path:      relPath,
		Size:      size,
		Timestamp: NowMS(),
	}

	// Send to debouncer
	w.debouncer.Add(fileEvent)
}

// handleDebouncedEvent processes a debounced event
func (w *Watcher) handleDebouncedEvent(event FileEvent) {
	// Compute partial hash for add/change events
	if event.Type == EventTypeAdd || event.Type == EventTypeChange {
		fullPath := filepath.Join(w.filesPath, event.Path)
		if hash, err := computePartialHash(fullPath); err == nil {
			event.PartialHash = hash
		}
	}

	// Add to buffer
	w.mu.Lock()
	w.eventBuffer = append(w.eventBuffer, event)
	// Keep buffer size reasonable
	if len(w.eventBuffer) > 10000 {
		w.eventBuffer = w.eventBuffer[len(w.eventBuffer)-5000:]
	}
	w.mu.Unlock()

	// Dispatch to subscribers
	w.dispatcher.Dispatch(event)

	log.Printf("[Watcher] Event: %s %s", event.Type, event.Path)
}

// RunScan performs a full directory scan
func (w *Watcher) RunScan() {
	w.mu.Lock()
	if w.isScanning {
		w.mu.Unlock()
		return
	}
	w.isScanning = true
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.isScanning = false
		w.lastScan = NowMS()
		w.mu.Unlock()
	}()

	log.Println("[Watcher] Starting directory scan...")

	fileCount := 0
	for _, watchPath := range w.watchPaths {
		count := w.scanDirectory(watchPath)
		fileCount += count
	}

	w.mu.Lock()
	w.fileCount = fileCount
	w.mu.Unlock()

	log.Printf("[Watcher] Scan complete: %d files found", fileCount)
}

// scanDirectory scans a directory and emits add events for files
func (w *Watcher) scanDirectory(root string) int {
	count := 0

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		// Get relative path
		relPath, err := filepath.Rel(w.filesPath, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		// Emit add event (through debouncer)
		event := FileEvent{
			Type:      EventTypeAdd,
			Path:      relPath,
			Size:      info.Size(),
			Timestamp: NowMS(),
		}

		// Compute partial hash
		if hash, err := computePartialHash(path); err == nil {
			event.PartialHash = hash
		}

		// Dispatch directly (skip debouncer for scan)
		w.dispatcher.Dispatch(event)
		count++

		return nil
	})

	return count
}

// GetRecentEvents returns events since a given timestamp
func (w *Watcher) GetRecentEvents(sinceMS int64, limit int) []FileEvent {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make([]FileEvent, 0)
	for _, event := range w.eventBuffer {
		if event.Timestamp > sinceMS {
			result = append(result, event)
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}

	return result
}

// GetStatus returns watcher status
func (w *Watcher) GetStatus() ScanStatusResponse {
	w.mu.RLock()
	defer w.mu.RUnlock()

	status := "running"
	if !w.isRunning {
		status = "stopped"
	}

	return ScanStatusResponse{
		Status:    status,
		Scanning:  w.isScanning,
		LastScan:  w.lastScan,
		FileCount: w.fileCount,
	}
}

// computePartialHash computes SHA-256 hash of first 64KB of a file
func computePartialHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	buffer := make([]byte, PartialHashSize)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", err
	}

	hasher.Write(buffer[:n])
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
