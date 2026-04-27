package mounts

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/metazla/meta-core/internal/config"
)

// Manager handles mount configuration and status
type Manager struct {
	config      *config.Config
	mu          sync.RWMutex
	filesPath   string
	mountsDir   string
	poller      *Poller
	statsPoller *StatsPoller
}

// NewManager creates a new mount manager
func NewManager(cfg *config.Config) (*Manager, error) {
	m := &Manager{
		config:    cfg,
		filesPath: cfg.FilesPath,
		mountsDir: cfg.MountsDir,
	}

	// Ensure directories exist
	if err := m.ensureDirs(); err != nil {
		return nil, fmt.Errorf("failed to create mount directories: %w", err)
	}

	// One-shot migration for legacy NFS entries — disable rather than fail to
	// start, so operators can fix their config at their own pace.
	if err := m.migrateLegacyMounts(); err != nil {
		log.Printf("[Mounts] legacy migration warning: %v", err)
	}

	return m, nil
}

// migrateLegacyMounts disables any mount entries with type="nfs". The native
// NFS handler was removed when the project consolidated on rclone; an NFS
// entry left enabled would loop the watcher with mount errors.
func (m *Manager) migrateLegacyMounts() error {
	mountsFile, err := m.readConfig()
	if err != nil {
		return err
	}

	dirty := false
	for i := range mountsFile.Mounts {
		mnt := &mountsFile.Mounts[i]
		if string(mnt.Type) == "nfs" {
			if mnt.Enabled || mnt.DesiredMounted {
				log.Printf("[Mounts] WARNING: disabling legacy NFS mount %q (%s) — NFS support was removed; please re-add via SMB or a pre-configured rclone remote", mnt.Name, mnt.ID)
				mnt.Enabled = false
				mnt.DesiredMounted = false
				dirty = true
			}
		}
	}

	if dirty {
		return m.writeConfig(mountsFile)
	}
	return nil
}

// SetPoller sets the poller for the manager
func (m *Manager) SetPoller(poller *Poller) {
	m.poller = poller
}

// SetStatsPoller sets the IO stats poller for the manager. ListMounts and
// GetMount will merge the latest sampled stats into MountStatus.IOStats when
// set; remains nil-safe when stats are disabled.
func (m *Manager) SetStatsPoller(sp *StatsPoller) {
	m.statsPoller = sp
}

// ensureDirs creates required directories
func (m *Manager) ensureDirs() error {
	dirs := []string{
		m.mountsDir,
		m.config.MountsErrorDir(),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}

// readConfig reads the mounts configuration file
func (m *Manager) readConfig() (*MountsFile, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	data, err := os.ReadFile(m.config.MountsFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &MountsFile{Version: 1, Mounts: []MountConfig{}}, nil
		}
		return nil, err
	}

	var mountsFile MountsFile
	if err := json.Unmarshal(data, &mountsFile); err != nil {
		return nil, err
	}

	return &mountsFile, nil
}

// writeConfig writes the mounts configuration file
func (m *Manager) writeConfig(mountsFile *MountsFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureDirs(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(mountsFile, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.config.MountsFilePath(), data, 0644)
}

// IsMounted checks if a path is currently mounted by parsing /proc/mounts
// Note: Uses /proc/mounts instead of findmnt for Alpine compatibility
func (m *Manager) IsMounted(mountPath string) bool {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return false
	}

	// Clean the mount path (remove trailing slash)
	cleanPath := strings.TrimSuffix(mountPath, "/")

	// Parse each line of /proc/mounts
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			// Field 1 is the mount point
			mountPoint := strings.TrimSuffix(fields[1], "/")
			if mountPoint == cleanPath {
				return true
			}
		}
	}
	return false
}

// ReadError reads the error file for a mount
func (m *Manager) ReadError(id string) (string, error) {
	errorFile := filepath.Join(m.config.MountsErrorDir(), id+".error")
	data, err := os.ReadFile(errorFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 1 {
		// Skip timestamp line, return rest
		return strings.Join(lines[1:], "\n"), nil
	}
	return "", nil
}

// SanitizeName sanitizes a mount name for use as a directory name
func SanitizeName(name string) string {
	// Convert to lowercase
	result := strings.ToLower(name)

	// Replace non-alphanumeric chars with hyphens
	re := regexp.MustCompile(`[^a-z0-9-_]`)
	result = re.ReplaceAllString(result, "-")

	// Collapse multiple hyphens
	re = regexp.MustCompile(`-+`)
	result = re.ReplaceAllString(result, "-")

	// Remove leading/trailing hyphens
	result = strings.Trim(result, "-")

	// Limit length
	if len(result) > 64 {
		result = result[:64]
	}

	return result
}

// ObscurePassword obscures a password using rclone
func ObscurePassword(password string) (string, error) {
	// Escape special characters for shell
	escapedPass := strings.ReplaceAll(password, `"`, `\"`)

	cmd := exec.Command("rclone", "obscure", escapedPass)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to obscure password: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// ListMounts returns all mounts with their current status
func (m *Manager) ListMounts() ([]MountStatus, error) {
	statuses, err := m.listMountsWithoutPollingStatus()
	if err != nil {
		return nil, err
	}

	// Add polling status from poller (done separately to avoid deadlock)
	if m.poller != nil {
		for i := range statuses {
			pollingStatus := m.poller.GetPollingStatus(statuses[i].ID)
			statuses[i].PollingActive = pollingStatus.Active
			statuses[i].LastPolledScan = pollingStatus.LastScan
			statuses[i].Scanning = pollingStatus.Scanning
			statuses[i].CurrentScanStartedAt = pollingStatus.CurrentScanStartedAt
			statuses[i].LastScanDurationMs = pollingStatus.LastScanDurationMs
			statuses[i].NextScanAt = pollingStatus.NextScanAt
		}
	}
	if m.statsPoller != nil {
		for i := range statuses {
			statuses[i].IOStats = m.statsPoller.GetStats(statuses[i].ID)
		}
	}

	return statuses, nil
}

// listMountsWithoutPollingStatus returns mounts without querying the poller
// This is used internally by the poller to avoid deadlocks
func (m *Manager) listMountsWithoutPollingStatus() ([]MountStatus, error) {
	mountsFile, err := m.readConfig()
	if err != nil {
		return nil, err
	}

	statuses := make([]MountStatus, len(mountsFile.Mounts))
	for i, mount := range mountsFile.Mounts {
		mounted := m.IsMounted(mount.MountPath)
		errMsg, _ := m.ReadError(mount.ID)

		statuses[i] = MountStatus{
			MountConfig: mount,
			Mounted:     mounted,
			Error:       errMsg,
			LastChecked: NowMS(),
		}
	}

	return statuses, nil
}

// GetMount returns a single mount by ID
func (m *Manager) GetMount(id string) (*MountStatus, error) {
	mountsFile, err := m.readConfig()
	if err != nil {
		return nil, err
	}

	for _, mount := range mountsFile.Mounts {
		if mount.ID == id {
			mounted := m.IsMounted(mount.MountPath)
			errMsg, _ := m.ReadError(mount.ID)

			status := &MountStatus{
				MountConfig: mount,
				Mounted:     mounted,
				Error:       errMsg,
				LastChecked: NowMS(),
			}

			// Add polling status from poller (safe since GetMount isn't called from poller with lock held)
			if m.poller != nil {
				pollingStatus := m.poller.GetPollingStatus(mount.ID)
				status.PollingActive = pollingStatus.Active
				status.LastPolledScan = pollingStatus.LastScan
				status.Scanning = pollingStatus.Scanning
				status.CurrentScanStartedAt = pollingStatus.CurrentScanStartedAt
				status.LastScanDurationMs = pollingStatus.LastScanDurationMs
				status.NextScanAt = pollingStatus.NextScanAt
			}
			if m.statsPoller != nil {
				status.IOStats = m.statsPoller.GetStats(mount.ID)
			}

			return status, nil
		}
	}

	return nil, nil
}

// CreateMount creates a new mount configuration
func (m *Manager) CreateMount(req *CreateMountRequest) (*MountStatus, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("mount name is required")
	}

	// "nfs" used to be valid; the native handler is gone. Reject loudly so the
	// caller knows their integration needs to migrate to a Samba re-export or
	// switch to a supported protocol.
	if req.Type == MountType("nfs") {
		return nil, fmt.Errorf("NFS mounts are no longer supported — please use SMB or a pre-configured rclone remote")
	}

	if req.Type != MountTypeSMB && req.Type != MountTypeRclone {
		return nil, fmt.Errorf("valid mount type (smb, rclone) is required")
	}

	// Validate type-specific fields
	switch req.Type {
	case MountTypeSMB:
		if req.SMBServer == "" || req.SMBShare == "" {
			return nil, fmt.Errorf("SMB server and share are required")
		}
	case MountTypeRclone:
		if req.RcloneRemote == "" {
			return nil, fmt.Errorf("rclone remote is required")
		}
	}

	// Generate ID and mount path
	id := uuid.New().String()
	safeName := SanitizeName(req.Name)
	mountPath := filepath.Join(m.filesPath, safeName)

	// Check if path already exists
	mountsFile, err := m.readConfig()
	if err != nil {
		return nil, err
	}

	for _, mount := range mountsFile.Mounts {
		if mount.MountPath == mountPath {
			return nil, fmt.Errorf("mount path %s already configured", mountPath)
		}
	}

	// Default enabled to true if not specified
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	// Polling defaults on for both supported types — neither SMB-via-rclone nor
	// generic rclone delivers kernel inotify, so a periodic re-scan is the only
	// way to pick up upstream changes. Explicit PollingEnabled in the request
	// wins.
	pollingEnabled := true
	if req.PollingEnabled != nil {
		pollingEnabled = *req.PollingEnabled
	}
	pollingIntervalMs := DefaultPollingIntervalMs
	if req.PollingIntervalMs != nil && *req.PollingIntervalMs >= MinPollingIntervalMs {
		pollingIntervalMs = *req.PollingIntervalMs
	}

	mount := MountConfig{
		ID:                id,
		Name:              req.Name,
		Type:              req.Type,
		Enabled:           enabled,
		DesiredMounted:    enabled, // Auto-mount if enabled
		MountPath:         mountPath,
		CacheMaxSize:      req.CacheMaxSize,
		CacheMaxAge:       req.CacheMaxAge,
		DirCacheTime:      req.DirCacheTime,
		PollingEnabled:    pollingEnabled,
		PollingIntervalMs: pollingIntervalMs,
	}

	// Type-specific fields
	switch req.Type {
	case MountTypeSMB:
		mount.SMBServer = req.SMBServer
		mount.SMBShare = req.SMBShare
		mount.SMBUsername = req.SMBUsername
		mount.SMBDomain = req.SMBDomain
		if req.SMBPassword != "" {
			obscured, err := ObscurePassword(req.SMBPassword)
			if err != nil {
				return nil, fmt.Errorf("failed to secure password: %w", err)
			}
			mount.SMBPasswordObscured = obscured
		}
	case MountTypeRclone:
		mount.RcloneRemote = req.RcloneRemote
		mount.RclonePath = req.RclonePath
	}

	mountsFile.Mounts = append(mountsFile.Mounts, mount)
	if err := m.writeConfig(mountsFile); err != nil {
		return nil, err
	}

	log.Printf("[Mounts] Created mount config: %s (%s) -> %s", mount.Name, mount.Type, mount.MountPath)

	// Notify poller about new mount
	if m.poller != nil {
		m.poller.NotifyMountChanged()
	}

	return &MountStatus{
		MountConfig: mount,
		Mounted:     false,
		LastChecked: NowMS(),
	}, nil
}

// UpdateMount updates mount configuration (for polling settings)
func (m *Manager) UpdateMount(id string, updates map[string]interface{}) (*MountStatus, error) {
	mountsFile, err := m.readConfig()
	if err != nil {
		return nil, err
	}

	index := -1
	for i, mount := range mountsFile.Mounts {
		if mount.ID == id {
			index = i
			break
		}
	}

	if index == -1 {
		return nil, fmt.Errorf("mount not found")
	}

	// Apply updates
	mount := &mountsFile.Mounts[index]

	if val, ok := updates["pollingEnabled"]; ok {
		if enabled, ok := val.(bool); ok {
			mount.PollingEnabled = enabled
		}
	}

	if val, ok := updates["pollingIntervalMs"]; ok {
		// Handle both int and float64 (JSON numbers are float64)
		switch v := val.(type) {
		case int:
			if v >= MinPollingIntervalMs {
				mount.PollingIntervalMs = v
			}
		case float64:
			if int(v) >= MinPollingIntervalMs {
				mount.PollingIntervalMs = int(v)
			}
		}
	}

	// VFS cache knobs — strings round-tripped to rclone unchanged, so we
	// don't validate format here; rclone rejects bad values at mount time.
	if val, ok := updates["cacheMaxSize"]; ok {
		if s, ok := val.(string); ok {
			mount.CacheMaxSize = s
		}
	}
	if val, ok := updates["cacheMaxAge"]; ok {
		if s, ok := val.(string); ok {
			mount.CacheMaxAge = s
		}
	}
	if val, ok := updates["dirCacheTime"]; ok {
		if s, ok := val.(string); ok {
			mount.DirCacheTime = s
		}
	}

	if err := m.writeConfig(mountsFile); err != nil {
		return nil, err
	}

	log.Printf("[Mounts] Updated mount config: %s", mount.Name)

	// Notify poller about config change
	if m.poller != nil {
		m.poller.NotifyMountChanged()
	}

	// Return updated status
	return m.GetMount(id)
}

// RequestMount sets desiredMounted to true
func (m *Manager) RequestMount(id string) error {
	mountsFile, err := m.readConfig()
	if err != nil {
		return err
	}

	for i, mount := range mountsFile.Mounts {
		if mount.ID == id {
			mountsFile.Mounts[i].DesiredMounted = true
			if err := m.writeConfig(mountsFile); err != nil {
				return err
			}
			log.Printf("[Mounts] Mount requested: %s", mount.Name)

			// Notify poller about state change
			if m.poller != nil {
				m.poller.NotifyMountChanged()
			}

			return nil
		}
	}

	return fmt.Errorf("mount not found")
}

// RequestUnmount sets desiredMounted to false
func (m *Manager) RequestUnmount(id string) error {
	mountsFile, err := m.readConfig()
	if err != nil {
		return err
	}

	for i, mount := range mountsFile.Mounts {
		if mount.ID == id {
			mountsFile.Mounts[i].DesiredMounted = false
			if err := m.writeConfig(mountsFile); err != nil {
				return err
			}
			log.Printf("[Mounts] Unmount requested: %s", mount.Name)

			// Notify poller about state change
			if m.poller != nil {
				m.poller.NotifyMountChanged()
			}

			return nil
		}
	}

	return fmt.Errorf("mount not found")
}

// WaitForUnmount waits for a mount to be unmounted
func (m *Manager) WaitForUnmount(mountPath string, timeoutMS int) bool {
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		if !m.IsMounted(mountPath) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// DeleteMount removes a mount configuration
func (m *Manager) DeleteMount(id string) error {
	mountsFile, err := m.readConfig()
	if err != nil {
		return err
	}

	var mount *MountConfig
	index := -1
	for i, mnt := range mountsFile.Mounts {
		if mnt.ID == id {
			mount = &mnt
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("mount not found")
	}

	// Request unmount first
	mountsFile.Mounts[index].DesiredMounted = false
	if err := m.writeConfig(mountsFile); err != nil {
		return err
	}

	// Wait for unmount (15 seconds max)
	m.WaitForUnmount(mount.MountPath, 15000)

	// Remove from config
	mountsFile.Mounts = append(mountsFile.Mounts[:index], mountsFile.Mounts[index+1:]...)
	if err := m.writeConfig(mountsFile); err != nil {
		return err
	}

	// Clean up error file
	errorFile := filepath.Join(m.config.MountsErrorDir(), id+".error")
	os.Remove(errorFile)

	// Try to remove mount directory (will fail if not empty, which is fine)
	os.Remove(mount.MountPath)

	log.Printf("[Mounts] Deleted mount: %s", mount.Name)
	return nil
}

// ListRcloneRemotes lists available rclone remotes
func (m *Manager) ListRcloneRemotes() ([]RcloneRemote, error) {
	// Call rclone RC API
	cmd := exec.Command("curl", "-s", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-u", "admin:admin",
		"http://127.0.0.1:5572/config/listremotes")

	output, err := cmd.Output()
	if err != nil {
		log.Printf("[Mounts] Failed to list rclone remotes: %v", err)
		return []RcloneRemote{}, nil
	}

	var response struct {
		Remotes []string `json:"remotes"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return []RcloneRemote{}, nil
	}

	remotes := make([]RcloneRemote, 0, len(response.Remotes))
	for _, name := range response.Remotes {
		// Strip trailing colon if present
		cleanName := strings.TrimSuffix(name, ":")

		// Get type for this remote
		remoteType := m.getRcloneRemoteType(cleanName)

		remotes = append(remotes, RcloneRemote{
			Name: cleanName,
			Type: remoteType,
		})
	}

	return remotes, nil
}

// getRcloneRemoteType gets the type of an rclone remote
func (m *Manager) getRcloneRemoteType(name string) string {
	// Build request body
	body := fmt.Sprintf(`{"name":"%s"}`, name)

	cmd := exec.Command("curl", "-s", "-X", "POST",
		"-H", "Content-Type: application/json",
		"-u", "admin:admin",
		"-d", body,
		"http://127.0.0.1:5572/config/get")

	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	var response map[string]interface{}
	if err := json.Unmarshal(output, &response); err != nil {
		return "unknown"
	}

	if typeStr, ok := response["type"].(string); ok {
		return typeStr
	}

	return "unknown"
}

// Basic auth helper for rclone API
func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}
