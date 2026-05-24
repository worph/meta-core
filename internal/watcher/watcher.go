package watcher

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/storage"
)


// Watcher provides file scanning and event dispatching
type Watcher struct {
	config        *config.Config
	debouncer     *Debouncer
	dispatcher    *Dispatcher
	filesPath     string
	stateRegistry *StateRegistry
	storage       *storage.Client

	mu          sync.RWMutex
	eventBuffer []FileEvent
}

// SetStorageClient lets the watcher persist the (path, size, mtime, midhash)
// tuple to Redis whenever it computes a midhash. Without this, only the
// in-memory state registry knows about the file — Redis would only learn
// about it via downstream consumers of the event stream.
func (w *Watcher) SetStorageClient(s *storage.Client) {
	w.storage = s
}

// writeFileTuple persists path/size/mtime/midhash for a freshly-hashed file.
// Caller already has all four values from its existing os.Stat / FileInfo —
// no extra IO is performed here beyond the Redis writes themselves.
//
// Flow (see docs/uuid-rooted-metadata.md):
//   - Look up the midhash in the reverse index (cid:midhash256:… → uuid)
//   - If unknown, Mint a fresh UUID root and write the midhash256 field
//     (the SetProperty hook auto-registers the alias)
//   - If known, this is a duplicate physical file for the same content —
//     record the new path in the duplicates set and refresh size/mtime
//
// This is also the inverse-index that lets a fresh meta-core boot skip
// re-mid-hashing files whose (path, size, mtime) match what's in Redis,
// via HydrateStateFromStorage below.
func (w *Watcher) writeFileTuple(absPath string, size, mtimeNano int64, midhash string) {
	if w.storage == nil || midhash == "" {
		return
	}
	token := "midhash256:" + midhash

	uuid, err := w.storage.GetByCID(token)
	if err != nil {
		log.Printf("[Watcher] GetByCID(%s): %v", token, err)
		return
	}

	if uuid == "" {
		// New content. Mint sets filePath/sizeByte/mtimeNano; SetProperty
		// then writes midhash256 — and the cid_resolution.go hook on
		// SetProperty registers the reverse-index alias for us.
		uuid, err = w.storage.Mint(absPath, size, mtimeNano)
		if err != nil {
			log.Printf("[Watcher] Mint(%s): %v", absPath, err)
			return
		}
		if err := w.storage.SetProperty(uuid, "midhash256", midhash); err != nil {
			log.Printf("[Watcher] SetProperty midhash256 for %s: %v", uuid, err)
		}
		return
	}

	// Known content. If this is a new physical path for the same content,
	// record it as a duplicate. Otherwise refresh size/mtime — same content,
	// same path, but the file may have been rewritten with a fresh mtime.
	existing, _ := w.storage.GetProperty(uuid, "filePath")
	if existing != absPath {
		if _, err := w.storage.AddDuplicatePath(uuid, absPath); err != nil {
			log.Printf("[Watcher] AddDuplicatePath %s @ %s: %v", uuid, absPath, err)
		}
		return
	}
	if _, err := w.storage.MergeMetadataFlat(uuid, map[string]string{
		"sizeByte":  strconv.FormatInt(size, 10),
		"mtimeNano": strconv.FormatInt(mtimeNano, 10),
	}); err != nil {
		log.Printf("[Watcher] refresh tuple for %s: %v", uuid, err)
	}
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
		if hash, err := ComputeMidHash256(fullPath, event.Size); err == nil {
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
		curSize := info.Size()
		curMtime := info.ModTime().UnixNano()
		event := FileEvent{
			Type:      EventTypeAdd,
			Path:      relPath,
			Size:      curSize,
			Timestamp: NowMS(),
		}

		// Compute midhash256 — uses the size we already have from Walk's
		// FileInfo, so no extra os.Stat is needed.
		if hash, err := ComputeMidHash256(path, curSize); err == nil {
			event.MidHash256 = hash
			w.writeFileTuple(path, curSize, curMtime, hash)
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

			// Compute midhash256 — size is already known from the walk's
			// FileInfo, no extra stat needed.
			if hash, err := ComputeMidHash256(path, currentSize); err == nil {
				event.MidHash256 = hash
				w.writeFileTuple(path, currentSize, currentMtime, hash)
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

			// Compute midhash256 — reuse the size we already have.
			if hash, err := ComputeMidHash256(path, currentSize); err == nil {
				event.MidHash256 = hash
				w.writeFileTuple(path, currentSize, currentMtime, hash)
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

// WatcherRoot pairs a watcher's id with the absolute path it watches. The
// hydrator uses Root to decide which Redis-tracked file belongs to which
// in-memory WatcherState.
type WatcherRoot struct {
	ID   string
	Root string
}

// HydrateStateFromStorage repopulates the per-watcher in-memory state from
// the (filePath, sizeByte, mtimeNano) tuples in Redis. After this runs, the
// next scan tick will see existing files as "known" and skip
// ComputeMidHash256 for any whose size+mtime match — turning a cold-start
// rescan from minutes to seconds on a populated library.
//
// Files whose filePath doesn't fall under any watcher's root are silently
// skipped; files missing any tuple field are skipped too (a partial record
// can't seed valid state).
//
// Returns counts so the caller can log / surface them.
func (w *Watcher) HydrateStateFromStorage(roots []WatcherRoot) (loaded, skipped int, err error) {
	if w.storage == nil || !w.storage.IsConnected() {
		return 0, 0, nil
	}

	tuples, err := w.storage.GetTuplesForAllFiles()
	if err != nil {
		return 0, 0, err
	}

	// Sort roots by length descending so longest-prefix wins when paths
	// nest (e.g. /files/watch/foo and /files/watch).
	sortedRoots := make([]WatcherRoot, len(roots))
	copy(sortedRoots, roots)
	for i := 1; i < len(sortedRoots); i++ {
		for j := i; j > 0 && len(sortedRoots[j].Root) > len(sortedRoots[j-1].Root); j-- {
			sortedRoots[j], sortedRoots[j-1] = sortedRoots[j-1], sortedRoots[j]
		}
	}

	// State keys are relative to w.filesPath (matching ScanDirectoryDiff's
	// Rel(w.filesPath, path) convention). The watcher root only decides
	// *which* per-watcher state map this file lives in.
	for _, t := range tuples {
		matched := false
		for _, r := range sortedRoots {
			if t.FilePath == r.Root || strings.HasPrefix(t.FilePath, r.Root+"/") {
				rel, err := filepath.Rel(w.filesPath, t.FilePath)
				if err != nil {
					rel = t.FilePath
				}
				rel = filepath.ToSlash(rel)
				ws := w.stateRegistry.GetOrCreate(r.ID)
				ws.Set(rel, &FileState{
					Size:      t.Size,
					MtimeNano: t.MtimeNano,
					MidHash:   t.MidHash256,
				})
				loaded++
				matched = true
				break
			}
		}
		if !matched {
			skipped++
		}
	}
	return loaded, skipped, nil
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

