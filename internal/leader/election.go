package leader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/metazla/meta-core/internal/config"
)

// LeaderLockInfo contains all URLs and metadata for the leader
type LeaderLockInfo struct {
	Hostname  string `json:"hostname"`
	BaseUrl   string `json:"baseUrl"`
	ApiUrl    string `json:"apiUrl"`   // meta-core API (port 9000)
	RedisUrl  string `json:"redisUrl"`
	WebdavUrl string `json:"webdavUrl"`
	Timestamp int64  `json:"timestamp"`
	PID       int    `json:"pid"`
}

// Role represents the current role of this instance
type Role string

const (
	RoleUnknown  Role = "unknown"
	RoleLeader   Role = "leader"
	RoleFollower Role = "follower"
)

// Election handles leader election using flock on shared filesystem
type Election struct {
	config *config.Config

	lockFile   *os.File
	role       Role
	leaderInfo *LeaderLockInfo
	mu         sync.RWMutex

	redisManager *RedisManager
	storage      StorageConnector

	// Callbacks
	onBecomeLeader   func()
	onBecomeFollower func(info *LeaderLockInfo)
	onLeaderLost     func()

	// Lifecycle
	stopChan     chan struct{}
	isShuttingDown bool
	wg           sync.WaitGroup
}

// StorageConnector interface for connecting to Redis
type StorageConnector interface {
	Connect(url string) error
	Close() error
}

// NewElection creates a new leader election instance
func NewElection(cfg *config.Config) *Election {
	return &Election{
		config:       cfg,
		role:         RoleUnknown,
		redisManager: NewRedisManager(cfg),
		stopChan:     make(chan struct{}),
	}
}

// SetStorageConnector sets the storage connector for Redis connections
func (e *Election) SetStorageConnector(s StorageConnector) {
	e.storage = s
}

// OnBecomeLeader sets callback for when this instance becomes leader
func (e *Election) OnBecomeLeader(fn func()) {
	e.onBecomeLeader = fn
}

// OnBecomeFollower sets callback for when this instance becomes follower
func (e *Election) OnBecomeFollower(fn func(info *LeaderLockInfo)) {
	e.onBecomeFollower = fn
}

// OnLeaderLost sets callback for when leadership is lost
func (e *Election) OnLeaderLost(fn func()) {
	e.onLeaderLost = fn
}

// Start begins the leader election process
func (e *Election) Start() error {
	log.Println("[Election] Starting leader election...")

	// Ensure lock directory exists
	lockDir := filepath.Dir(e.config.LockFilePath())
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Try to acquire the lock
	acquired, err := e.tryAcquireLock()
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}

	if acquired {
		if err := e.transitionToLeader(); err != nil {
			return err
		}
	} else {
		if err := e.transitionToFollower(); err != nil {
			return err
		}
	}

	// Start health check loop
	e.wg.Add(1)
	go e.healthCheckLoop()

	return nil
}

// Stop gracefully shuts down the election
func (e *Election) Stop() error {
	log.Println("[Election] Stopping leader election...")
	e.isShuttingDown = true
	close(e.stopChan)

	e.wg.Wait()

	// Stop Redis if we were leader
	if e.Role() == RoleLeader {
		if err := e.redisManager.Stop(); err != nil {
			log.Printf("[Election] Error stopping Redis: %v", err)
		}
	}

	// Release the lock
	e.releaseLock()

	return nil
}

// Role returns the current role (thread-safe)
func (e *Election) Role() Role {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.role
}

// LeaderInfo returns the current leader info (thread-safe)
func (e *Election) LeaderInfo() *LeaderLockInfo {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.leaderInfo == nil {
		return nil
	}
	// Return a copy
	info := *e.leaderInfo
	return &info
}

// IsLeader returns true if this instance is the leader
func (e *Election) IsLeader() bool {
	return e.Role() == RoleLeader
}

// tryAcquireLock attempts to acquire exclusive flock on the lock file
func (e *Election) tryAcquireLock() (bool, error) {
	lockPath := e.config.LockFilePath()

	// Open or create the lock file
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		return false, fmt.Errorf("failed to open lock file: %w", err)
	}

	// Try non-blocking exclusive flock
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			log.Println("[Election] Lock is held by another process")
			return false, nil
		}
		return false, fmt.Errorf("flock failed: %w", err)
	}

	e.lockFile = f
	log.Printf("[Election] Acquired flock on %s", lockPath)
	return true, nil
}

// releaseLock releases the flock
func (e *Election) releaseLock() {
	if e.lockFile != nil {
		syscall.Flock(int(e.lockFile.Fd()), syscall.LOCK_UN)
		e.lockFile.Close()
		e.lockFile = nil
		log.Println("[Election] Released flock")
	}
}

// transitionToLeader handles becoming the leader
func (e *Election) transitionToLeader() error {
	log.Println("[Election] Transitioning to LEADER role")

	e.mu.Lock()
	e.role = RoleLeader
	e.mu.Unlock()

	// Start Redis
	if err := e.redisManager.Start(); err != nil {
		return fmt.Errorf("failed to start Redis: %w", err)
	}

	// Wait for Redis to be ready
	if err := e.redisManager.WaitForReady(30 * time.Second); err != nil {
		return fmt.Errorf("Redis not ready: %w", err)
	}

	// Build and write leader info
	info := e.buildLeaderInfo()
	e.mu.Lock()
	e.leaderInfo = info
	e.mu.Unlock()

	if err := e.writeLeaderInfo(info); err != nil {
		return fmt.Errorf("failed to write leader info: %w", err)
	}

	// Connect storage to local Redis
	if e.storage != nil {
		if err := e.storage.Connect(info.RedisUrl); err != nil {
			log.Printf("[Election] Warning: failed to connect storage: %v", err)
		}
	}

	// Notify callback
	if e.onBecomeLeader != nil {
		e.onBecomeLeader()
	}

	log.Println("[Election] Now acting as LEADER")
	return nil
}

// transitionToFollower handles becoming a follower
func (e *Election) transitionToFollower() error {
	log.Println("[Election] Transitioning to FOLLOWER role")

	e.mu.Lock()
	e.role = RoleFollower
	e.mu.Unlock()

	// Read leader info
	info, err := e.readLeaderInfo()
	if err != nil {
		return fmt.Errorf("failed to read leader info: %w", err)
	}

	e.mu.Lock()
	e.leaderInfo = info
	e.mu.Unlock()

	if info != nil {
		log.Printf("[Election] Leader is at %s (Redis: %s)", info.ApiUrl, info.RedisUrl)

		// Connect storage to leader's Redis
		if e.storage != nil {
			if err := e.storage.Connect(info.RedisUrl); err != nil {
				log.Printf("[Election] Warning: failed to connect to leader: %v", err)
			}
		}

		// Notify callback
		if e.onBecomeFollower != nil {
			e.onBecomeFollower(info)
		}
	}

	log.Println("[Election] Now acting as FOLLOWER")
	return nil
}

// buildLeaderInfo creates leader info for this instance
func (e *Election) buildLeaderInfo() *LeaderLockInfo {
	ip := getLocalIP()
	hostname, _ := os.Hostname()

	// Use BaseURL if configured, otherwise construct from IP
	baseUrl := e.config.BaseURL
	if baseUrl == "" {
		baseUrl = fmt.Sprintf("http://%s:%d", ip, e.config.APIPort)
	}

	return &LeaderLockInfo{
		Hostname:  hostname,
		BaseUrl:   baseUrl,
		ApiUrl:    fmt.Sprintf("http://%s:%d", ip, e.config.HTTPPort),
		RedisUrl:  fmt.Sprintf("redis://%s:%d", ip, e.config.RedisPort),
		WebdavUrl: fmt.Sprintf("http://%s:%d/webdav", ip, e.config.APIPort),
		Timestamp: time.Now().UnixMilli(),
		PID:       os.Getpid(),
	}
}

// writeLeaderInfo atomically writes leader API URL to file (plain text)
// Only writes if the content has changed to avoid triggering file watchers unnecessarily
func (e *Election) writeLeaderInfo(info *LeaderLockInfo) error {
	infoPath := e.config.InfoFilePath()

	// Write just the API URL as plain text
	newData := []byte(info.ApiUrl)

	// Check if file already has the same content
	existingData, err := os.ReadFile(infoPath)
	if err == nil && string(existingData) == string(newData) {
		// Content unchanged, skip write to avoid triggering file watchers
		return nil
	}

	// Content changed or file doesn't exist, write atomically
	tempPath := infoPath + ".tmp"
	if err := os.WriteFile(tempPath, newData, 0644); err != nil {
		return err
	}

	if err := os.Rename(tempPath, infoPath); err != nil {
		return err
	}

	log.Printf("[Election] Wrote leader API URL to %s: %s", infoPath, info.ApiUrl)
	return nil
}

// readLeaderInfo reads leader API URL from file and fetches full info via /urls API
func (e *Election) readLeaderInfo() (*LeaderLockInfo, error) {
	infoPath := e.config.InfoFilePath()

	data, err := os.ReadFile(infoPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// File contains plain text API URL
	apiUrl := strings.TrimSpace(string(data))
	if apiUrl == "" {
		return nil, nil
	}

	// Fetch full info from /urls API
	return e.fetchLeaderInfoFromAPI(apiUrl)
}

// fetchLeaderInfoFromAPI calls the /urls API to get full leader info
func (e *Election) fetchLeaderInfoFromAPI(apiUrl string) (*LeaderLockInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiUrl+"/urls", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call /urls API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/urls API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Parse URLsResponse format
	var urlsResp struct {
		Hostname  string `json:"hostname"`
		BaseUrl   string `json:"baseUrl"`
		ApiUrl    string `json:"apiUrl"`
		RedisUrl  string `json:"redisUrl"`
		WebdavUrl string `json:"webdavUrl"`
		IsLeader  bool   `json:"isLeader"`
	}
	if err := json.Unmarshal(body, &urlsResp); err != nil {
		return nil, fmt.Errorf("failed to parse /urls response: %w", err)
	}

	// Convert to LeaderLockInfo
	return &LeaderLockInfo{
		Hostname:  urlsResp.Hostname,
		BaseUrl:   urlsResp.BaseUrl,
		ApiUrl:    urlsResp.ApiUrl,
		RedisUrl:  urlsResp.RedisUrl,
		WebdavUrl: urlsResp.WebdavUrl,
		Timestamp: time.Now().UnixMilli(),
		PID:       0, // Unknown for remote leader
	}, nil
}

// healthCheckLoop periodically checks health and updates timestamps
func (e *Election) healthCheckLoop() {
	defer e.wg.Done()

	ticker := time.NewTicker(time.Duration(e.config.HealthCheckIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopChan:
			return
		case <-ticker.C:
			e.performHealthCheck()
		}
	}
}

// performHealthCheck checks health based on current role
func (e *Election) performHealthCheck() {
	switch e.Role() {
	case RoleLeader:
		// Update timestamp in leader info
		e.updateLeaderTimestamp()

		// Check if Redis is still running
		if !e.redisManager.IsRunning() {
			log.Println("[Election] Redis not running, attempting restart...")
			if err := e.redisManager.Start(); err != nil {
				log.Printf("[Election] Failed to restart Redis: %v", err)
			}
		}

	case RoleFollower:
		// Re-read leader info in case it changed
		info, err := e.readLeaderInfo()
		if err != nil {
			log.Printf("[Election] Failed to read leader info: %v", err)
			return
		}

		if info != nil {
			e.mu.Lock()
			e.leaderInfo = info
			e.mu.Unlock()
		}
	}
}

// updateLeaderTimestamp updates the timestamp in leader info
func (e *Election) updateLeaderTimestamp() {
	e.mu.Lock()
	if e.leaderInfo != nil {
		e.leaderInfo.Timestamp = time.Now().UnixMilli()
		info := *e.leaderInfo
		e.mu.Unlock()

		if err := e.writeLeaderInfo(&info); err != nil {
			log.Printf("[Election] Failed to update timestamp: %v", err)
		}
	} else {
		e.mu.Unlock()
	}
}

// getLocalIP returns the local IP address
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		hostname, _ := os.Hostname()
		return hostname
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	hostname, _ := os.Hostname()
	return hostname
}
