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

// ServiceInfo matches the TypeScript ServiceInfo interface
type ServiceInfo struct {
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	API           string            `json:"api"`
	Status        string            `json:"status"`
	PID           int               `json:"pid"`
	Hostname      string            `json:"hostname"`
	StartedAt     string            `json:"startedAt"`
	LastHeartbeat string            `json:"lastHeartbeat"`
	Capabilities  []string          `json:"capabilities"`
	Endpoints     map[string]string `json:"endpoints"`
}

// Service handles service registration and discovery
type Service struct {
	config      *config.Config
	servicesDir string
	serviceFile string
	info        *ServiceInfo
	mu          sync.RWMutex

	stopChan chan struct{}
	wg       sync.WaitGroup
}

// NewService creates a new service discovery instance
func NewService(cfg *config.Config) *Service {
	return &Service{
		config:      cfg,
		servicesDir: cfg.ServicesDir(),
		serviceFile: filepath.Join(cfg.ServicesDir(), cfg.ServiceName+".json"),
		stopChan:    make(chan struct{}),
	}
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

	apiBase := fmt.Sprintf("http://%s:%d", ip, s.config.APIPort)
	metaCoreBase := fmt.Sprintf("http://%s:%d", ip, s.config.HTTPPort)

	return &ServiceInfo{
		Name:          s.config.ServiceName,
		Version:       s.config.ServiceVersion,
		API:           apiBase,
		Status:        "running",
		PID:           os.Getpid(),
		Hostname:      hostname,
		StartedAt:     time.Now().UTC().Format(time.RFC3339),
		LastHeartbeat: time.Now().UTC().Format(time.RFC3339),
		Capabilities:  []string{"meta-core"},
		Endpoints: map[string]string{
			// meta-core sidecar endpoints (port 9000)
			"health":   metaCoreBase + "/health",
			"meta":     metaCoreBase + "/meta",
			"leader":   metaCoreBase + "/leader",
			"services": metaCoreBase + "/services",
			// main service endpoints (port 80)
			"api":      apiBase + "/api",
			"webdav":   apiBase + "/webdav",
			"callback": apiBase + "/api/plugins/callback",
		},
	}
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

// heartbeat updates the last heartbeat timestamp
func (s *Service) heartbeat() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.info == nil {
		return
	}

	s.info.LastHeartbeat = time.Now().UTC().Format(time.RFC3339)
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
func (s *Service) Discover(name string) (*ServiceInfo, error) {
	filePath := filepath.Join(s.servicesDir, name+".json")

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var info ServiceInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	// Check if service is stale
	lastHeartbeat, err := time.Parse(time.RFC3339, info.LastHeartbeat)
	if err == nil {
		staleThreshold := time.Duration(s.config.StaleThresholdMS) * time.Millisecond
		if time.Since(lastHeartbeat) > staleThreshold {
			info.Status = "stale"
		}
	}

	return &info, nil
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

		name := entry.Name()[:len(entry.Name())-5] // Remove .json extension
		info, err := s.Discover(name)
		if err != nil {
			log.Printf("[Discovery] Failed to read service %s: %v", name, err)
			continue
		}

		if info != nil {
			services = append(services, info)
		}
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
