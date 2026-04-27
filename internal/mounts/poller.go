package mounts

import (
	"log"
	"sync"
	"time"
)

// ScanFunc is a function type for scanning a mount path
type ScanFunc func(path string) (int, error)

// IdleFactor controls the adaptive scheduler's duty cycle. The next scan is
// delayed by max(pollingIntervalMs, lastScanDurationMs * IdleFactor), which
// pins steady-state work to roughly 1/(IdleFactor+1) of wall-clock time. With
// IdleFactor=9 a 60s scan triggers a 9-min rest (≈10% duty cycle), keeping
// large remote shares (CORN, etc.) from monopolising the network/CPU.
const IdleFactor = 9

// InitialScanDelay is how long a freshly-started poller waits before its first
// scan. Avoids hammering the system at container startup when many mounts come
// up at once.
const InitialScanDelay = 2 * time.Second

// ReconcileInterval drives the periodic resync loop that keeps poller
// goroutines aligned with mount state. NotifyMountChanged covers config edits,
// but the kernel mount itself is brought up asynchronously by mount-watcher.sh
// — without periodic reconciliation, the poller would never observe the
// transition from `mounted=false` to `mounted=true` and never start scanning.
const ReconcileInterval = 10 * time.Second

// mountPoller manages polling for a single mount
type mountPoller struct {
	mountID  string
	stopChan chan struct{}
	stopped  bool
	mu       sync.Mutex
}

// PollingStatus holds the polling state for a mount
type PollingStatus struct {
	Active               bool  `json:"active"`
	LastScan             int64 `json:"lastScan,omitempty"`             // ms; completion timestamp
	Scanning             bool  `json:"scanning,omitempty"`             // true while a scan is in flight
	CurrentScanStartedAt int64 `json:"currentScanStartedAt,omitempty"` // ms; start of in-flight scan
	LastScanDurationMs   int64 `json:"lastScanDurationMs,omitempty"`   // duration of the previous scan
	NextScanAt           int64 `json:"nextScanAt,omitempty"`           // ms; planned start of next adaptive scan
}

// Poller manages per-mount polling goroutines
type Poller struct {
	manager  *Manager
	scanFunc ScanFunc

	pollers map[string]*mountPoller // key: mount ID
	status  map[string]*PollingStatus

	running         bool
	reconcileStop   chan struct{}
	mu              sync.RWMutex
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
	p.reconcileStop = make(chan struct{})

	log.Println("[Poller] Starting mount polling service")

	// Initial sync with mount configurations
	go p.syncWithMounts()

	// Periodic reconciliation — picks up mount-watcher.sh transitions
	// (mounted/unmounted) that NotifyMountChanged doesn't observe.
	go p.reconcileLoop(p.reconcileStop)

	return nil
}

// reconcileLoop runs syncWithMounts on a fixed cadence so the poller catches
// mount state changes initiated outside of CreateMount/UpdateMount/etc.
func (p *Poller) reconcileLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			p.syncWithMounts()
		}
	}
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

	if p.reconcileStop != nil {
		close(p.reconcileStop)
		p.reconcileStop = nil
	}

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
			// Calculate interval (used as the floor for adaptive scheduling)
			interval := mount.PollingIntervalMs
			if interval < MinPollingIntervalMs {
				interval = DefaultPollingIntervalMs
			}

			if !exists {
				// Start new poller
				log.Printf("[Poller] Starting poller for mount %s (interval floor: %dms, idle factor: %dx)", mount.Name, interval, IdleFactor)
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

// startPollerForMount starts the adaptive polling goroutine for a mount.
//
// The loop runs scans sequentially: scan → measure duration → sleep
// max(intervalMs, duration*IdleFactor) → repeat. This makes scans
// self-debouncing (no overlap) and bounds steady-state work to ~10% duty
// cycle on slow shares, while keeping fresh checks on small mounts via the
// configured interval floor.
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

	floor := time.Duration(intervalMs) * time.Millisecond

	go func() {
		// Initial delay so a freshly-mounted share doesn't get hammered the
		// instant the poller starts.
		nextDelay := InitialScanDelay
		p.setNextScanAt(mountID, time.Now().Add(nextDelay))

		for {
			select {
			case <-poller.stopChan:
				return
			case <-time.After(nextDelay):
			}

			// Re-check stop after the wake-up to avoid running a scan during shutdown.
			select {
			case <-poller.stopChan:
				return
			default:
			}

			duration, err := p.executeScan(mountID, mountPath)
			if err != nil {
				log.Printf("[Poller] Scan failed for mount %s: %v", mountID, err)
			}

			// Adaptive backoff: idle for IdleFactor × last duration, with the
			// configured interval as a floor so tiny mounts still scan often.
			nextDelay = duration * IdleFactor
			if nextDelay < floor {
				nextDelay = floor
			}
			p.setNextScanAt(mountID, time.Now().Add(nextDelay))
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
		status.NextScanAt = 0
	}
}

// executeScan runs scanFunc with timing + status tracking. Both the adaptive
// loop and manual TriggerScan funnel through here so the UI sees a consistent
// "scanning / last duration / last scan" picture regardless of which path
// kicked it off. Returns the scan duration so the caller can compute the next
// adaptive delay.
func (p *Poller) executeScan(mountID, mountPath string) (time.Duration, error) {
	if p.scanFunc == nil {
		return 0, nil
	}

	start := time.Now()
	startMs := start.UnixMilli()

	p.mu.Lock()
	status, exists := p.status[mountID]
	if !exists {
		status = &PollingStatus{}
		p.status[mountID] = status
	}
	status.Scanning = true
	status.CurrentScanStartedAt = startMs
	p.mu.Unlock()

	fileCount, err := p.scanFunc(mountPath)

	end := time.Now()
	duration := end.Sub(start)

	p.mu.Lock()
	if status, exists := p.status[mountID]; exists {
		status.Scanning = false
		status.CurrentScanStartedAt = 0
		status.LastScan = end.UnixMilli()
		status.LastScanDurationMs = duration.Milliseconds()
	}
	p.mu.Unlock()

	if err == nil {
		log.Printf("[Poller] Poll scan complete for mount %s: %d files in %s", mountID, fileCount, duration.Round(time.Millisecond))
	}

	return duration, err
}

// setNextScanAt records the projected start time of the next adaptive scan
// (used purely for the UI countdown — the loop's own timer is authoritative).
func (p *Poller) setNextScanAt(mountID string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	status, exists := p.status[mountID]
	if !exists {
		status = &PollingStatus{}
		p.status[mountID] = status
	}
	status.NextScanAt = at.UnixMilli()
}

// runPollScan is kept for backwards compatibility with any external caller —
// new code should go through executeScan via the adaptive loop.
func (p *Poller) runPollScan(mountID, mountPath string) {
	_, _ = p.executeScan(mountID, mountPath)
}

// TriggerScan triggers an immediate scan for a mount (the "Scan Now" button).
// Runs synchronously; concurrent calls with the adaptive loop are possible
// but rare in practice — both paths update the same status atomically.
func (p *Poller) TriggerScan(mountID, mountPath string) (int, error) {
	if p.scanFunc == nil {
		return 0, nil
	}

	start := time.Now()
	startMs := start.UnixMilli()

	p.mu.Lock()
	status, exists := p.status[mountID]
	if !exists {
		status = &PollingStatus{}
		p.status[mountID] = status
	}
	status.Scanning = true
	status.CurrentScanStartedAt = startMs
	p.mu.Unlock()

	fileCount, err := p.scanFunc(mountPath)

	end := time.Now()
	duration := end.Sub(start)

	p.mu.Lock()
	if status, exists := p.status[mountID]; exists {
		status.Scanning = false
		status.CurrentScanStartedAt = 0
		status.LastScan = end.UnixMilli()
		status.LastScanDurationMs = duration.Milliseconds()
	}
	p.mu.Unlock()

	return fileCount, err
}

// GetPollingStatus returns the polling status for a mount
func (p *Poller) GetPollingStatus(mountID string) *PollingStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if status, exists := p.status[mountID]; exists {
		return &PollingStatus{
			Active:               status.Active,
			LastScan:             status.LastScan,
			Scanning:             status.Scanning,
			CurrentScanStartedAt: status.CurrentScanStartedAt,
			LastScanDurationMs:   status.LastScanDurationMs,
			NextScanAt:           status.NextScanAt,
		}
	}
	return &PollingStatus{}
}

// NotifyMountChanged notifies the poller that a mount's state has changed
func (p *Poller) NotifyMountChanged() {
	go p.syncWithMounts()
}
