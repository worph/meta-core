package discovery

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/metazla/meta-core/internal/config"
)

// CleanupStats holds statistics about the cleanup process
type CleanupStats struct {
	LastRunTime     string `json:"lastRunTime,omitempty"`
	ServicesRemoved int    `json:"servicesRemoved"`
	TotalRuns       int    `json:"totalRuns"`
	Errors          int    `json:"errors"`
}

// Cleaner handles periodic cleanup of dead service registration files
type Cleaner struct {
	config      *config.Config
	servicesDir string
	ownFile     string // Our own service file (never delete)

	stats    CleanupStats
	stopChan chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

// NewCleaner creates a new service cleaner
func NewCleaner(cfg *config.Config) *Cleaner {
	hostname, _ := os.Hostname()
	return &Cleaner{
		config:      cfg,
		servicesDir: cfg.ServicesDir(),
		ownFile:     filepath.Join(cfg.ServicesDir(), cfg.ServiceName+"-"+hostname+".json"),
		stopChan:    make(chan struct{}),
	}
}

// Start begins the cleanup loop
func (c *Cleaner) Start() error {
	log.Printf("[Cleaner] Starting dead service cleanup (interval: %dms, threshold: %dms)",
		c.config.CleanupIntervalMS, c.config.DeadServiceThresholdMS)

	c.wg.Add(1)
	go c.cleanupLoop()

	return nil
}

// Stop stops the cleanup loop
func (c *Cleaner) Stop() error {
	log.Println("[Cleaner] Stopping dead service cleanup...")
	close(c.stopChan)
	c.wg.Wait()
	return nil
}

// cleanupLoop runs the periodic cleanup
func (c *Cleaner) cleanupLoop() {
	defer c.wg.Done()

	// Run an initial cleanup shortly after starting
	initialDelay := time.NewTimer(30 * time.Second)
	select {
	case <-c.stopChan:
		initialDelay.Stop()
		return
	case <-initialDelay.C:
		c.runCleanup()
	}

	ticker := time.NewTicker(time.Duration(c.config.CleanupIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.runCleanup()
		}
	}
}

// runCleanup scans the services directory and removes dead services
func (c *Cleaner) runCleanup() {
	c.mu.Lock()
	c.stats.TotalRuns++
	c.stats.LastRunTime = time.Now().UTC().Format(time.RFC3339)
	c.mu.Unlock()

	entries, err := os.ReadDir(c.servicesDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Cleaner] Failed to read services directory: %v", err)
			c.mu.Lock()
			c.stats.Errors++
			c.mu.Unlock()
		}
		return
	}

	threshold := time.Duration(c.config.DeadServiceThresholdMS) * time.Millisecond
	removedCount := 0

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(c.servicesDir, entry.Name())

		// Never delete our own service file
		if filePath == c.ownFile {
			continue
		}

		// Check if service is dead
		isDead, serviceName, age := c.isServiceDead(filePath, threshold)
		if isDead {
			if err := os.Remove(filePath); err != nil {
				log.Printf("[Cleaner] Failed to remove dead service file %s: %v", entry.Name(), err)
				c.mu.Lock()
				c.stats.Errors++
				c.mu.Unlock()
			} else {
				ageMinutes := age.Minutes()
				log.Printf("[Cleaner] Removed dead service: %s (last heartbeat: %.1f minutes ago)", serviceName, ageMinutes)
				removedCount++
			}
		}
	}

	if removedCount > 0 {
		c.mu.Lock()
		c.stats.ServicesRemoved += removedCount
		c.mu.Unlock()
		log.Printf("[Cleaner] Cleanup complete: removed %d dead service(s)", removedCount)
	}
}

// isServiceDead checks if a service file represents a dead service
// Returns: isDead, serviceName, age since last heartbeat
func (c *Cleaner) isServiceDead(filePath string, threshold time.Duration) (bool, string, time.Duration) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		// Can't read file - treat as dead to clean up corrupted files
		log.Printf("[Cleaner] Cannot read service file %s: %v (treating as dead)", filepath.Base(filePath), err)
		return true, filepath.Base(filePath), threshold
	}

	var info ServiceInfo
	if err := json.Unmarshal(data, &info); err != nil {
		// Corrupted JSON - skip rather than delete (might be recoverable)
		log.Printf("[Cleaner] Skipping corrupted service file %s: %v", filepath.Base(filePath), err)
		return false, "", 0
	}

	// Parse last heartbeat timestamp
	if info.LastHeartbeat == "" {
		// No heartbeat timestamp - treat as dead
		return true, info.Name, threshold
	}

	lastHeartbeat, err := time.Parse(time.RFC3339, info.LastHeartbeat)
	if err != nil {
		// Invalid timestamp - treat as dead
		log.Printf("[Cleaner] Invalid timestamp in service file %s: %v (treating as dead)", filepath.Base(filePath), err)
		return true, info.Name, threshold
	}

	age := time.Since(lastHeartbeat)
	if age > threshold {
		return true, info.Name, age
	}

	return false, info.Name, age
}

// Stats returns a copy of the current cleanup statistics
func (c *Cleaner) Stats() CleanupStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CleanupStats{
		LastRunTime:     c.stats.LastRunTime,
		ServicesRemoved: c.stats.ServicesRemoved,
		TotalRuns:       c.stats.TotalRuns,
		Errors:          c.stats.Errors,
	}
}
