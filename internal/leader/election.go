package leader

import (
	"fmt"
	"net"
	"os"
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

// RoleProvider provides role and leader info
// This interface is used by the API server to get role information
// without needing the full election logic (which is now in bash)
type RoleProvider interface {
	Role() Role
	LeaderInfo() *LeaderLockInfo
	IsLeader() bool
}

// LocalRoleProvider provides role info for this instance
// It reads the role from environment and builds leader info locally
// This is used when running as the leader (started by leader-election.sh)
type LocalRoleProvider struct {
	config     *config.Config
	role       Role
	leaderInfo *LeaderLockInfo
}

// NewLocalRoleProvider creates a RoleProvider for the local instance
// It reads META_CORE_ROLE from environment (set by leader-election.sh)
func NewLocalRoleProvider(cfg *config.Config) *LocalRoleProvider {
	roleStr := os.Getenv("META_CORE_ROLE")
	var role Role
	switch roleStr {
	case "leader":
		role = RoleLeader
	case "follower":
		role = RoleFollower
	default:
		// Default to leader if not set (for backward compatibility)
		role = RoleLeader
	}

	provider := &LocalRoleProvider{
		config: cfg,
		role:   role,
	}

	// Build leader info (always, since we're the leader when running)
	provider.leaderInfo = provider.buildLeaderInfo()

	return provider
}

// Role returns the current role
func (p *LocalRoleProvider) Role() Role {
	return p.role
}

// LeaderInfo returns the leader info
func (p *LocalRoleProvider) LeaderInfo() *LeaderLockInfo {
	if p.leaderInfo == nil {
		return nil
	}
	// Return a copy
	info := *p.leaderInfo
	// Update timestamp on each call
	info.Timestamp = time.Now().UnixMilli()
	return &info
}

// IsLeader returns true if this instance is the leader
func (p *LocalRoleProvider) IsLeader() bool {
	return p.role == RoleLeader
}

// buildLeaderInfo creates leader info for this instance
func (p *LocalRoleProvider) buildLeaderInfo() *LeaderLockInfo {
	ip := getLocalIP()
	hostname, _ := os.Hostname()

	// Use BaseURL if configured, otherwise construct from IP
	baseUrl := p.config.BaseURL
	if baseUrl == "" {
		baseUrl = fmt.Sprintf("http://%s:%d", ip, p.config.APIPort)
	}

	return &LeaderLockInfo{
		Hostname:  hostname,
		BaseUrl:   baseUrl,
		ApiUrl:    fmt.Sprintf("http://%s:%d", ip, p.config.HTTPPort),
		RedisUrl:  fmt.Sprintf("redis://%s:%d", ip, p.config.RedisPort),
		WebdavUrl: fmt.Sprintf("http://%s:%d/webdav", ip, p.config.APIPort),
		Timestamp: time.Now().UnixMilli(),
		PID:       os.Getpid(),
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
