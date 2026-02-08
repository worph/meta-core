package watchers

import (
	"log"
	"sync"
	"time"

	"github.com/metazla/meta-core/internal/watcher"
)

// ScanFunc is the function type for scanning a directory
type ScanFunc func(path string) int

// watcherPoller manages polling for a single watcher
type watcherPoller struct {
	watcherID string
	stopChan  chan struct{}
	stopped   bool
	mu        sync.Mutex
}

// RuntimeStatus holds the runtime state for a watcher
type RuntimeStatus struct {
	Active     bool
	LastScan   int64
	FileCount  int
	IsScanning bool
}

// Poller manages per-watcher polling goroutines
type Poller struct {
	manager    *Manager
	scanner    *watcher.Watcher
	dispatcher *watcher.Dispatcher

	pollers map[string]*watcherPoller // key: watcher ID
	status  map[string]*RuntimeStatus

	running bool
	mu      sync.RWMutex
}

// NewPoller creates a new watchers poller
func NewPoller(manager *Manager, scanner *watcher.Watcher, dispatcher *watcher.Dispatcher) *Poller {
	p := &Poller{
		manager:    manager,
		scanner:    scanner,
		dispatcher: dispatcher,
		pollers:    make(map[string]*watcherPoller),
		status:     make(map[string]*RuntimeStatus),
	}

	// Register callback for watcher changes
	manager.SetOnChanged(p.syncWithWatchers)

	return p
}

// Start begins the polling service
func (p *Poller) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil
	}
	p.running = true

	log.Println("[WatcherPoller] Starting file watcher polling service")

	// Initial sync with watcher configurations
	go p.syncWithWatchers()

	return nil
}

// Stop stops all polling goroutines
func (p *Poller) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}
	p.running = false

	log.Println("[WatcherPoller] Stopping file watcher polling service")

	// Stop all pollers
	for id, poller := range p.pollers {
		p.stopPoller(poller)
		delete(p.pollers, id)
	}

	return nil
}

// SyncWithWatchers synchronizes pollers with current watcher configurations
func (p *Poller) syncWithWatchers() {
	p.mu.RLock()
	running := p.running
	p.mu.RUnlock()

	if !running {
		return
	}

	// Get current watchers
	watchers := p.manager.List()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check running state after acquiring lock
	if !p.running {
		return
	}

	// Track which watchers we've seen
	seenWatchers := make(map[string]bool)

	for _, w := range watchers {
		seenWatchers[w.ID] = true

		existingPoller, exists := p.pollers[w.ID]

		if w.Enabled {
			// Calculate interval
			interval := w.IntervalMs
			if interval < MinIntervalMs {
				interval = DefaultIntervalMs
			}

			if !exists {
				// Start new poller
				log.Printf("[WatcherPoller] Starting poller for watcher %s (path: %s, interval: %dms)", w.ID, w.Path, interval)
				p.startPollerForWatcher(w.ID, w.Path, interval)
			}
		} else if exists {
			// Stop existing poller
			log.Printf("[WatcherPoller] Stopping poller for watcher %s", w.ID)
			p.stopPoller(existingPoller)
			delete(p.pollers, w.ID)
		}
	}

	// Stop pollers for watchers that no longer exist
	for id, poller := range p.pollers {
		if !seenWatchers[id] {
			log.Printf("[WatcherPoller] Stopping poller for removed watcher %s", id)
			p.stopPoller(poller)
			delete(p.pollers, id)
		}
	}
}

// startPollerForWatcher starts a polling goroutine for a watcher
func (p *Poller) startPollerForWatcher(watcherID, path string, intervalMs int) {
	poller := &watcherPoller{
		watcherID: watcherID,
		stopChan:  make(chan struct{}),
	}
	p.pollers[watcherID] = poller

	// Initialize status
	if _, exists := p.status[watcherID]; !exists {
		p.status[watcherID] = &RuntimeStatus{}
	}
	p.status[watcherID].Active = true

	go func() {
		// Run initial scan immediately
		p.runPollScan(watcherID, path)

		ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-poller.stopChan:
				return
			case <-ticker.C:
				p.runPollScan(watcherID, path)
			}
		}
	}()
}

// stopPoller stops a watcher poller
func (p *Poller) stopPoller(poller *watcherPoller) {
	poller.mu.Lock()
	defer poller.mu.Unlock()

	if poller.stopped {
		return
	}
	poller.stopped = true
	close(poller.stopChan)

	// Update status
	if status, exists := p.status[poller.watcherID]; exists {
		status.Active = false
	}
}

// runPollScan executes a differential scan for a watcher
func (p *Poller) runPollScan(watcherID, path string) {
	if p.scanner == nil {
		return
	}

	// Mark as scanning
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.IsScanning = true
	}
	p.mu.Unlock()

	// Perform differential scan
	result := p.scanner.ScanDirectoryDiff(path, watcherID)

	// Update status
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.LastScan = NowMS()
		status.FileCount = result.Total
		status.IsScanning = false
	}
	p.mu.Unlock()

	// Only log if there were changes
	if result.Added > 0 || result.Changed > 0 || result.Deleted > 0 {
		log.Printf("[WatcherPoller] Diff scan for %s: +%d ~%d -%d (total: %d)",
			watcherID, result.Added, result.Changed, result.Deleted, result.Total)
	}
}

// TriggerScan triggers an immediate differential scan for a watcher
func (p *Poller) TriggerScan(watcherID string) (int, error) {
	w, err := p.manager.Get(watcherID)
	if err != nil {
		return 0, err
	}

	if p.scanner == nil {
		return 0, nil
	}

	// Mark as scanning
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.IsScanning = true
	} else {
		p.status[watcherID] = &RuntimeStatus{IsScanning: true}
	}
	p.mu.Unlock()

	// Perform differential scan
	result := p.scanner.ScanDirectoryDiff(w.Path, watcherID)

	// Update status
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.LastScan = NowMS()
		status.FileCount = result.Total
		status.IsScanning = false
	}
	p.mu.Unlock()

	log.Printf("[WatcherPoller] Triggered scan for %s: +%d ~%d -%d (total: %d)",
		watcherID, result.Added, result.Changed, result.Deleted, result.Total)

	return result.Total, nil
}

// TriggerScanAll triggers scans for all enabled watchers
func (p *Poller) TriggerScanAll() (int, error) {
	watchers := p.manager.List()
	totalCount := 0

	for _, w := range watchers {
		if w.Enabled {
			count, err := p.TriggerScan(w.ID)
			if err != nil {
				log.Printf("[WatcherPoller] Failed to scan watcher %s: %v", w.ID, err)
				continue
			}
			totalCount += count
		}
	}

	return totalCount, nil
}

// TriggerReset performs a full reset for a watcher:
// 1. Clears the Redis stream and emits a reset event
// 2. Clears the watcher's state
// 3. Performs a full scan (all files are "new")
func (p *Poller) TriggerReset(watcherID string) (int, error) {
	w, err := p.manager.Get(watcherID)
	if err != nil {
		return 0, err
	}

	if p.scanner == nil || p.dispatcher == nil {
		return 0, nil
	}

	log.Printf("[WatcherPoller] Triggering reset for watcher %s", watcherID)

	// Mark as scanning
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.IsScanning = true
	} else {
		p.status[watcherID] = &RuntimeStatus{IsScanning: true}
	}
	p.mu.Unlock()

	// Step 1: Emit reset event (this clears the stream first)
	if err := p.dispatcher.EmitReset(watcherID); err != nil {
		log.Printf("[WatcherPoller] Warning: failed to emit reset for %s: %v", watcherID, err)
	}

	// Step 2: Clear the watcher's state
	p.scanner.ClearState(watcherID)

	// Step 3: Clear event buffer
	p.scanner.ClearEventBuffer()

	// Step 4: Perform full scan (all files are now "new" since state is cleared)
	result := p.scanner.ScanDirectoryDiff(w.Path, watcherID)

	// Update status
	p.mu.Lock()
	if status, exists := p.status[watcherID]; exists {
		status.LastScan = NowMS()
		status.FileCount = result.Total
		status.IsScanning = false
	}
	p.mu.Unlock()

	log.Printf("[WatcherPoller] Reset complete for %s: %d files", watcherID, result.Total)

	return result.Total, nil
}

// TriggerResetAll performs a full reset for all enabled watchers
func (p *Poller) TriggerResetAll() (int, error) {
	watchers := p.manager.List()
	totalCount := 0
	isFirst := true

	for _, w := range watchers {
		if w.Enabled {
			// Only emit reset event once (for the first watcher)
			// Subsequent watchers just clear state and rescan
			if isFirst {
				count, err := p.TriggerReset(w.ID)
				if err != nil {
					log.Printf("[WatcherPoller] Failed to reset watcher %s: %v", w.ID, err)
					continue
				}
				totalCount += count
				isFirst = false
			} else {
				// Clear state and rescan without clearing stream again
				p.scanner.ClearState(w.ID)
				result := p.scanner.ScanDirectoryDiff(w.Path, w.ID)
				totalCount += result.Total
				log.Printf("[WatcherPoller] Reset scan for %s: %d files", w.ID, result.Total)
			}
		}
	}

	return totalCount, nil
}

// GetStatus returns the runtime status for a watcher
func (p *Poller) GetStatus(watcherID string) *RuntimeStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if status, exists := p.status[watcherID]; exists {
		return &RuntimeStatus{
			Active:     status.Active,
			LastScan:   status.LastScan,
			FileCount:  status.FileCount,
			IsScanning: status.IsScanning,
		}
	}
	return &RuntimeStatus{}
}

// GetWatcherStatuses returns all watchers with their runtime status
func (p *Poller) GetWatcherStatuses() []WatcherStatus {
	watchers := p.manager.List()
	result := make([]WatcherStatus, 0, len(watchers))

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, w := range watchers {
		status := WatcherStatus{
			WatcherConfig: w,
		}

		if rs, exists := p.status[w.ID]; exists {
			status.Active = rs.Active
			status.LastScan = rs.LastScan
			status.FileCount = rs.FileCount
			status.IsScanning = rs.IsScanning
		}

		result = append(result, status)
	}

	return result
}
