package discovery

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/metazla/meta-core/internal/config"
)

// ServiceInfo contains simplified service registration data
// URLs are obtained via the /urls API endpoint
type ServiceInfo struct {
	Name          string `json:"name"`
	Hostname      string `json:"hostname"`
	BaseUrl       string `json:"baseUrl"`
	Status        string `json:"status"`
	LastHeartbeat string `json:"lastHeartbeat"`
	Role          string `json:"role,omitempty"` // "leader", "follower", or empty (for non-meta-core services)
}

// RoleProvider interface for getting the current role
type RoleProvider interface {
	Role() string
}

// Service handles service registration and discovery
type Service struct {
	config       *config.Config
	servicesDir  string
	serviceFile  string
	info         *ServiceInfo
	roleProvider RoleProvider
	mu           sync.RWMutex

	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewService creates a new service discovery instance
func NewService(cfg *config.Config) *Service {
	hostname, _ := os.Hostname()
	return &Service{
		config:      cfg,
		servicesDir: cfg.ServicesDir(),
		serviceFile: filepath.Join(cfg.ServicesDir(), cfg.ServiceName+"-"+hostname+".json"),
		stopChan:    make(chan struct{}),
	}
}

// SetRoleProvider sets the role provider for this service
func (s *Service) SetRoleProvider(rp RoleProvider) {
	s.roleProvider = rp
}

// Start begins service registration and heartbeat
func (s *Service) Start() error {
	log.Printf("[Discovery] Starting service discovery for %s", s.config.ServiceName)

	// Ensure services directory exists
	if err := os.MkdirAll(s.servicesDir, 0755); err != nil {
		return fmt.Errorf("failed to create services directory: %w", err)
	}

	// Build and register service info
	s.info = s.buildServiceInfo()
	if err := s.register(); err != nil {
		return fmt.Errorf("failed to register service: %w", err)
	}

	// Start heartbeat loop
	s.wg.Add(1)
	go s.heartbeatLoop()

	return nil
}

// Stop stops service discovery and unregisters
func (s *Service) Stop() error {
	log.Println("[Discovery] Stopping service discovery...")
	close(s.stopChan)
	s.wg.Wait()

	// Unregister by removing service file
	if err := os.Remove(s.serviceFile); err != nil && !os.IsNotExist(err) {
		log.Printf("[Discovery] Failed to remove service file: %v", err)
	}

	return nil
}

// buildServiceInfo creates the service info for this instance
func (s *Service) buildServiceInfo() *ServiceInfo {
	hostname, _ := os.Hostname()
	ip := getLocalIP()

	// Use BASE_URL for external access, fall back to internal IP
	var baseUrl string
	if s.config.BaseURL != "" {
		baseUrl = s.config.BaseURL
	} else {
		baseUrl = fmt.Sprintf("http://%s:%d", ip, s.config.APIPort)
	}

	info := &ServiceInfo{
		Name:          s.config.ServiceName,
		Hostname:      hostname,
		BaseUrl:       baseUrl,
		Status:        "running",
		LastHeartbeat: time.Now().UTC().Format(time.RFC3339),
	}

	// Add role if role provider is set (e.g., for meta-core)
	if s.roleProvider != nil {
		info.Role = s.roleProvider.Role()
	}

	return info
}

// register writes service info to file
func (s *Service) register() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.writeServiceInfo(s.info)
}

// writeServiceInfo atomically writes service info to file
func (s *Service) writeServiceInfo(info *ServiceInfo) error {
	tempPath := s.serviceFile + ".tmp"

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempPath, s.serviceFile)
}

// heartbeatLoop periodically updates the last heartbeat
func (s *Service) heartbeatLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Duration(s.config.HeartbeatIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.heartbeat()
		}
	}
}

// heartbeat updates the last heartbeat timestamp and role
func (s *Service) heartbeat() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.info == nil {
		return
	}

	s.info.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)

	// Update role in case it changed (e.g., leader election)
	if s.roleProvider != nil {
		s.info.Role = s.roleProvider.Role()
	}

	if err := s.writeServiceInfo(s.info); err != nil {
		log.Printf("[Discovery] Failed to update heartbeat: %v", err)
	}
}

// UpdateStatus updates the service status
func (s *Service) UpdateStatus(status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.info == nil {
		return fmt.Errorf("service not registered")
	}

	s.info.Status = status
	s.info.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
	return s.writeServiceInfo(s.info)
}

// Discover finds a service by name
// Looks for files matching pattern: name-*.json (hostname-based naming)
func (s *Service) Discover(name string) (*ServiceInfo, error) {
	// First try exact match for backward compatibility
	exactPath := filepath.Join(s.servicesDir, name+".json")
	if data, err := os.ReadFile(exactPath); err == nil {
		var info ServiceInfo
		if err := json.Unmarshal(data, &info); err == nil {
			return s.checkStale(&info), nil
		}
	}

	// Search for hostname-based files: name-*.json
	pattern := filepath.Join(s.servicesDir, name+"-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	// Return the first valid service found
	for _, match := range matches {
		data, err := os.ReadFile(match)
		if err != nil {
			continue
		}

		var info ServiceInfo
		if err := json.Unmarshal(data, &info); err != nil {
			continue
		}

		return s.checkStale(&info), nil
	}

	return nil, nil
}

// checkStale marks a service as stale if heartbeat is too old
func (s *Service) checkStale(info *ServiceInfo) *ServiceInfo {
	lastHeartbeat, err := time.Parse(time.RFC3339, info.LastHeartbeat)
	if err == nil {
		staleThreshold := time.Duration(s.config.StaleThresholdMS) * time.Millisecond
		if time.Since(lastHeartbeat) > staleThreshold {
			info.Status = "stale"
		}
	}
	return info
}

// DiscoverAll finds all registered services
func (s *Service) DiscoverAll() ([]*ServiceInfo, error) {
	entries, err := os.ReadDir(s.servicesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*ServiceInfo{}, nil
		}
		return nil, err
	}

	var services []*ServiceInfo
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		filePath := filepath.Join(s.servicesDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("[Discovery] Failed to read service file %s: %v", entry.Name(), err)
			continue
		}

		var info ServiceInfo
		if err := json.Unmarshal(data, &info); err != nil {
			log.Printf("[Discovery] Failed to parse service file %s: %v", entry.Name(), err)
			continue
		}

		services = append(services, s.checkStale(&info))
	}

	return services, nil
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
