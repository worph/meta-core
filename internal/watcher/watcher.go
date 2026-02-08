package watcher

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/metazla/meta-core/internal/config"
)


// Watcher provides file scanning and event dispatching
type Watcher struct {
	config        *config.Config
	debouncer     *Debouncer
	dispatcher    *Dispatcher
	filesPath     string
	stateRegistry *StateRegistry

	mu          sync.RWMutex
	eventBuffer []FileEvent
}

// NewWatcher creates a new file watcher/scanner
func NewWatcher(cfg *config.Config, dispatcher *Dispatcher) (*Watcher, error) {
	debouncer := NewDebouncer(0) // No debouncing for polling-based scanning

	w := &Watcher{
		config:        cfg,
		debouncer:     debouncer,
		dispatcher:    dispatcher,
		filesPath:     cfg.FilesPath,
		stateRegistry: NewStateRegistry(),
		eventBuffer:   make([]FileEvent, 0, 1000),
	}

	return w, nil
}

// handleDebouncedEvent processes a debounced event
func (w *Watcher) handleDebouncedEvent(event FileEvent) {
	// Compute midhash256 for add/change events
	if event.Type == EventTypeAdd || event.Type == EventTypeChange {
		fullPath := filepath.Join(w.filesPath, event.Path)
		if hash, err := ComputeMidHash256(fullPath); err == nil {
			event.MidHash256 = hash
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
}

// ScanDirectory scans a directory and emits add events for files
// Returns the count of files found
func (w *Watcher) ScanDirectory(root string) int {
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

		// Emit add event
		event := FileEvent{
			Type:      EventTypeAdd,
			Path:      relPath,
			Size:      info.Size(),
			Timestamp: NowMS(),
		}

		// Compute midhash256
		if hash, err := ComputeMidHash256(path); err == nil {
			event.MidHash256 = hash
		}

		// Dispatch directly
		w.dispatcher.Dispatch(event)
		count++

		return nil
	})

	return count
}

// ScanDirectoryDiff scans a directory and only emits events for differences
// Returns the count of added, changed, and deleted files
func (w *Watcher) ScanDirectoryDiff(root string, watcherID string) DiffScanResult {
	result := DiffScanResult{}
	state := w.stateRegistry.GetOrCreate(watcherID)

	// Get snapshot of known files
	knownFiles := state.GetAll()
	seenFiles := make(map[string]bool)

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
		seenFiles[relPath] = true

		// Check current file state
		currentMtime := info.ModTime().UnixNano()
		currentSize := info.Size()

		prevState := knownFiles[relPath]

		if prevState == nil {
			// New file - emit add event
			event := FileEvent{
				Type:      EventTypeAdd,
				Path:      relPath,
				Size:      currentSize,
				Timestamp: NowMS(),
			}

			// Compute midhash256
			if hash, err := ComputeMidHash256(path); err == nil {
				event.MidHash256 = hash
			}

			// Dispatch event
			w.dispatcher.Dispatch(event)

			// Update state
			state.Set(relPath, &FileState{
				Size:      currentSize,
				MtimeNano: currentMtime,
				MidHash:   event.MidHash256,
			})

			result.Added++
		} else if prevState.Size != currentSize || prevState.MtimeNano != currentMtime {
			// File changed - emit change event
			event := FileEvent{
				Type:      EventTypeChange,
				Path:      relPath,
				Size:      currentSize,
				Timestamp: NowMS(),
			}

			// Compute midhash256
			if hash, err := ComputeMidHash256(path); err == nil {
				event.MidHash256 = hash
			}

			// Dispatch event
			w.dispatcher.Dispatch(event)

			// Update state
			state.Set(relPath, &FileState{
				Size:      currentSize,
				MtimeNano: currentMtime,
				MidHash:   event.MidHash256,
			})

			result.Changed++
		}
		// If file exists and hasn't changed, do nothing

		return nil
	})

	// Check for deleted files (in known but not seen)
	for relPath := range knownFiles {
		if !seenFiles[relPath] {
			// File deleted - emit delete event
			event := FileEvent{
				Type:      EventTypeDelete,
				Path:      relPath,
				Timestamp: NowMS(),
			}

			w.dispatcher.Dispatch(event)
			state.Delete(relPath)
			result.Deleted++
		}
	}

	result.Total = state.Count()
	return result
}

// ClearState clears the tracked state for a watcher
func (w *Watcher) ClearState(watcherID string) {
	w.stateRegistry.Clear(watcherID)
}

// ClearEventBuffer clears the in-memory event buffer
func (w *Watcher) ClearEventBuffer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.eventBuffer = make([]FileEvent, 0, 1000)
}

// GetStateRegistry returns the state registry for external access
func (w *Watcher) GetStateRegistry() *StateRegistry {
	return w.stateRegistry
}

// ScanMountPath scans a specific mount path and returns file count
// This is used by the mount poller to scan individual mounts
func (w *Watcher) ScanMountPath(mountPath string) (int, error) {
	// Validate path is under filesPath
	if !strings.HasPrefix(mountPath, w.filesPath) {
		// Try to normalize paths
		absMount, err := filepath.Abs(mountPath)
		if err == nil {
			absFiles, err := filepath.Abs(w.filesPath)
			if err == nil && strings.HasPrefix(absMount, absFiles) {
				mountPath = absMount
			}
		}
	}

	// Check directory exists
	info, err := os.Stat(mountPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[Watcher] Mount path %s does not exist, skipping", mountPath)
			return 0, nil
		}
		return 0, err
	}

	if !info.IsDir() {
		log.Printf("[Watcher] Mount path %s is not a directory, skipping", mountPath)
		return 0, nil
	}

	// Scan the directory
	log.Printf("[Watcher] Scanning mount path: %s", mountPath)
	count := w.ScanDirectory(mountPath)
	log.Printf("[Watcher] Mount scan complete: %s (%d files)", mountPath, count)

	return count, nil
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

// GetFilesPath returns the files path
func (w *Watcher) GetFilesPath() string {
	return w.filesPath
}

