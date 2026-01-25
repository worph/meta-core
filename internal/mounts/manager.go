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
	config    *config.Config
	mu        sync.RWMutex
	filesPath string
	mountsDir string
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

	return m, nil
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

// IsMounted checks if a path is currently mounted
func (m *Manager) IsMounted(mountPath string) bool {
	cmd := exec.Command("findmnt", "-n", mountPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
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

			return &MountStatus{
				MountConfig: mount,
				Mounted:     mounted,
				Error:       errMsg,
				LastChecked: NowMS(),
			}, nil
		}
	}

	return nil, nil
}

// CreateMount creates a new mount configuration
func (m *Manager) CreateMount(req *CreateMountRequest) (*MountStatus, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("mount name is required")
	}

	if req.Type != MountTypeNFS && req.Type != MountTypeSMB && req.Type != MountTypeRclone {
		return nil, fmt.Errorf("valid mount type (nfs, smb, rclone) is required")
	}

	// Validate type-specific fields
	switch req.Type {
	case MountTypeNFS:
		if req.NFSServer == "" || req.NFSPath == "" {
			return nil, fmt.Errorf("NFS server and path are required")
		}
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

	mount := MountConfig{
		ID:             id,
		Name:           req.Name,
		Type:           req.Type,
		Enabled:        enabled,
		DesiredMounted: enabled, // Auto-mount if enabled
		MountPath:      mountPath,
		Options:        req.Options,
	}

	// Type-specific fields
	switch req.Type {
	case MountTypeNFS:
		mount.NFSServer = req.NFSServer
		mount.NFSPath = req.NFSPath
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

	return &MountStatus{
		MountConfig: mount,
		Mounted:     false,
		LastChecked: NowMS(),
	}, nil
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
