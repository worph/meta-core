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
	Hostname          string `json:"hostname"`
	BaseUrl           string `json:"baseUrl"`
	ApiUrl            string `json:"apiUrl"`            // meta-core API (port 9000)
	RedisUrl          string `json:"redisUrl"`
	WebdavUrl         string `json:"webdavUrl"`         // External WebDAV URL (via baseUrl)
	WebdavUrlInternal string `json:"webdavUrlInternal"` // Internal WebDAV URL (direct to port 9000)
	Timestamp         int64  `json:"timestamp"`
	PID               int    `json:"pid"`
}

// LeaderInfoProvider provides leader info for this instance
// The Go binary only ever runs as leader (followers loop in bash and never start the binary)
type LeaderInfoProvider struct {
	config     *config.Config
	leaderInfo *LeaderLockInfo
}

// NewLeaderInfoProvider creates a LeaderInfoProvider for the local instance
func NewLeaderInfoProvider(cfg *config.Config) *LeaderInfoProvider {
	provider := &LeaderInfoProvider{
		config: cfg,
	}

	// Build leader info (always, since we're the leader when running)
	provider.leaderInfo = provider.buildLeaderInfo()

	return provider
}

// LeaderInfo returns the leader info
func (p *LeaderInfoProvider) LeaderInfo() *LeaderLockInfo {
	if p.leaderInfo == nil {
		return nil
	}
	// Return a copy
	info := *p.leaderInfo
	// Update timestamp on each call
	info.Timestamp = time.Now().UnixMilli()
	return &info
}

// buildLeaderInfo creates leader info for this instance
func (p *LeaderInfoProvider) buildLeaderInfo() *LeaderLockInfo {
	ip := getLocalIP()
	hostname, _ := os.Hostname()

	// Use BaseURL if configured, otherwise construct from IP
	baseUrl := p.config.BaseURL
	if baseUrl == "" {
		baseUrl = fmt.Sprintf("http://%s:%d", ip, p.config.APIPort)
	}

	// External WebDAV URL: for clients outside Docker (via nginx, potentially HTTPS)
	// Uses baseUrl which may include custom scheme/port
	webdavUrl := baseUrl + "/webdav"

	// Internal WebDAV URL: direct to Go WebDAV server (no nginx overhead)
	// Uses hostname (Docker DNS resolvable) and HTTPPort (9000)
	webdavUrlInternal := fmt.Sprintf("http://%s:%d/webdav", hostname, p.config.HTTPPort)

	return &LeaderLockInfo{
		Hostname:          hostname,
		BaseUrl:           baseUrl,
		ApiUrl:            fmt.Sprintf("http://%s:%d", ip, p.config.HTTPPort),
		// RedisUrl deliberately empty — api-mediated-access PR D removes
		// direct Redis exposure. Consumers route metadata I/O through
		// meta-core's HTTP API (/meta/{hash}*) and event streams through
		// SSE (/api/events/{files,meta}).
		RedisUrl:          "",
		WebdavUrl:         webdavUrl,
		WebdavUrlInternal: webdavUrlInternal,
		Timestamp:         time.Now().UnixMilli(),
		PID:               os.Getpid(),
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
