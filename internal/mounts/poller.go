package mounts

import (
	"log"
	"sync"
	"time"
)

// ScanFunc is a function type for scanning a mount path
type ScanFunc func(path string) (int, error)

// mountPoller manages polling for a single mount
type mountPoller struct {
	mountID  string
	stopChan chan struct{}
	stopped  bool
	mu       sync.Mutex
}

// PollingStatus holds the polling state for a mount
type PollingStatus struct {
	Active   bool  `json:"active"`
	LastScan int64 `json:"lastScan,omitempty"`
}

// Poller manages per-mount polling goroutines
type Poller struct {
	manager  *Manager
	scanFunc ScanFunc

	pollers map[string]*mountPoller // key: mount ID
	status  map[string]*PollingStatus

	running bool
	mu      sync.RWMutex
}

// NewPoller creates a new mount poller
func NewPoller(manager *Manager, scanFunc ScanFunc) *Poller {
	return &Poller{
		manager:  manager,
		scanFunc: scanFunc,
		pollers:  make(map[string]*mountPoller),
		status:   make(map[string]*PollingStatus),
	}
}

// Start begins the polling service
func (p *Poller) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil
	}
	p.running = true

	log.Println("[Poller] Starting mount polling service")

	// Initial sync with mount configurations
	go p.syncWithMounts()

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

	log.Println("[Poller] Stopping mount polling service")

	// Stop all pollers
	for id, poller := range p.pollers {
		p.stopPoller(poller)
		delete(p.pollers, id)
	}

	return nil
}

// SyncWithMounts synchronizes pollers with current mount configurations
func (p *Poller) SyncWithMounts() {
	p.syncWithMounts()
}

// syncWithMounts is the internal implementation
func (p *Poller) syncWithMounts() {
	p.mu.RLock()
	running := p.running
	p.mu.RUnlock()

	if !running {
		return
	}

	// Get current mounts BEFORE acquiring the write lock to avoid deadlock
	// (ListMounts calls GetPollingStatus which needs RLock)
	mounts, err := p.manager.listMountsWithoutPollingStatus()
	if err != nil {
		log.Printf("[Poller] Failed to list mounts: %v", err)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-check running state after acquiring lock
	if !p.running {
		return
	}

	// Track which mounts we've seen
	seenMounts := make(map[string]bool)

	for _, mount := range mounts {
		seenMounts[mount.ID] = true

		// Check if mount should be polled
		shouldPoll := mount.PollingEnabled && mount.Mounted

		existingPoller, exists := p.pollers[mount.ID]

		if shouldPoll {
			// Calculate interval
			interval := mount.PollingIntervalMs
			if interval < MinPollingIntervalMs {
				interval = DefaultPollingIntervalMs
			}

			if !exists {
				// Start new poller
				log.Printf("[Poller] Starting poller for mount %s (interval: %dms)", mount.Name, interval)
				p.startPollerForMount(mount.ID, mount.MountPath, interval)
			}
		} else if exists {
			// Stop existing poller
			log.Printf("[Poller] Stopping poller for mount %s", mount.Name)
			p.stopPoller(existingPoller)
			delete(p.pollers, mount.ID)
		}
	}

	// Stop pollers for mounts that no longer exist
	for id, poller := range p.pollers {
		if !seenMounts[id] {
			log.Printf("[Poller] Stopping poller for removed mount %s", id)
			p.stopPoller(poller)
			delete(p.pollers, id)
		}
	}
}

// startPollerForMount starts a polling goroutine for a mount
func (p *Poller) startPollerForMount(mountID, mountPath string, intervalMs int) {
	poller := &mountPoller{
		mountID:  mountID,
		stopChan: make(chan struct{}),
	}
	p.pollers[mountID] = poller

	// Initialize status
	if _, exists := p.status[mountID]; !exists {
		p.status[mountID] = &PollingStatus{}
	}
	p.status[mountID].Active = true

	go func() {
		ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-poller.stopChan:
				return
			case <-ticker.C:
				p.runPollScan(mountID, mountPath)
			}
		}
	}()
}

// stopPoller stops a mount poller
func (p *Poller) stopPoller(poller *mountPoller) {
	poller.mu.Lock()
	defer poller.mu.Unlock()

	if poller.stopped {
		return
	}
	poller.stopped = true
	close(poller.stopChan)

	// Update status
	if status, exists := p.status[poller.mountID]; exists {
		status.Active = false
	}
}

// runPollScan executes a scan for a mount
func (p *Poller) runPollScan(mountID, mountPath string) {
	if p.scanFunc == nil {
		return
	}

	fileCount, err := p.scanFunc(mountPath)
	if err != nil {
		log.Printf("[Poller] Scan failed for mount %s: %v", mountID, err)
		return
	}

	// Update status
	p.mu.Lock()
	if status, exists := p.status[mountID]; exists {
		status.LastScan = NowMS()
	}
	p.mu.Unlock()

	log.Printf("[Poller] Poll scan complete for mount %s: %d files", mountID, fileCount)
}

// TriggerScan triggers an immediate scan for a mount
func (p *Poller) TriggerScan(mountID, mountPath string) (int, error) {
	if p.scanFunc == nil {
		return 0, nil
	}

	fileCount, err := p.scanFunc(mountPath)
	if err != nil {
		return 0, err
	}

	// Update status
	p.mu.Lock()
	if status, exists := p.status[mountID]; exists {
		status.LastScan = NowMS()
	} else {
		p.status[mountID] = &PollingStatus{
			LastScan: NowMS(),
		}
	}
	p.mu.Unlock()

	return fileCount, nil
}

// GetPollingStatus returns the polling status for a mount
func (p *Poller) GetPollingStatus(mountID string) *PollingStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if status, exists := p.status[mountID]; exists {
		return &PollingStatus{
			Active:   status.Active,
			LastScan: status.LastScan,
		}
	}
	return &PollingStatus{}
}

// NotifyMountChanged notifies the poller that a mount's state has changed
func (p *Poller) NotifyMountChanged() {
	go p.syncWithMounts()
}
