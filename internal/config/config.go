package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for meta-core
type Config struct {
	// Core paths
	MetaCorePath string // Path to meta-core volume (default: /meta-core)
	FilesPath    string // Path to files volume (default: /files)

	// Service identification
	ServiceName    string // Service name (e.g., "meta-sort", "meta-fuse")
	ServiceVersion string // Service version (default: "1.0.0")
	APIPort        int    // Service HTTP port (for leader info)
	BaseURL        string // Base URL for stable service discovery
	PublicURL      string // Browser-facing URL for the service-bar nav; overrides BaseURL when set (e.g. a debug-direct port with no Caddy in front)

	// Redis configuration
	RedisPort int // Redis port (default: 6379)

	// HTTP API configuration
	HTTPPort int    // HTTP API port (default: 9000)
	HTTPHost string // HTTP API host (default: "0.0.0.0" — meta-core always runs in a container and must answer siblings)

	// Timing configuration
	HealthCheckIntervalMS    int // Health check interval in ms (default: 5000)
	HeartbeatIntervalMS      int // Service heartbeat interval in ms (default: 30000)
	StaleThresholdMS         int // Stale service threshold in ms (default: 60000)
	CleanupIntervalMS        int // Dead service cleanup interval in ms (default: 600000)
	DeadServiceThresholdMS   int // Dead service threshold in ms (default: 600000)

	// File watcher configuration
	WatchIntervalMS   int      // Polling interval for network mounts (default: 1000)
	DebounceMS        int      // File change debounce time (default: 30000)
	EnableFileWatcher bool     // Enable file watcher (default: true)

	// Mount configuration
	MountsDir string // Path to mounts configuration (default: /meta-core/mounts)
}

// Load creates a Config from environment variables
func Load() *Config {
	cfg := &Config{
		MetaCorePath:          getEnv("META_CORE_PATH", "/meta-core"),
		FilesPath:             getEnv("FILES_PATH", "/files"),
		ServiceName:           getEnv("SERVICE_NAME", "meta-core"),
		ServiceVersion:        getEnv("SERVICE_VERSION", "1.0.0"),
		APIPort:               getEnvInt("API_PORT", 8180),
		BaseURL:               getEnv("BASE_URL", ""),
		PublicURL:             getEnv("META_CORE_PUBLIC_URL", ""),
		RedisPort:             getEnvInt("REDIS_PORT", 6379),
		HTTPPort:              getEnvInt("META_CORE_HTTP_PORT", 9000),
		HTTPHost:              getEnv("META_CORE_HTTP_HOST", "0.0.0.0"),
		HealthCheckIntervalMS:  getEnvInt("HEALTH_CHECK_INTERVAL_MS", 5000),
		HeartbeatIntervalMS:   getEnvInt("HEARTBEAT_INTERVAL_MS", 30000),
		StaleThresholdMS:      getEnvInt("STALE_THRESHOLD_MS", 60000),
		CleanupIntervalMS:     getEnvInt("CLEANUP_INTERVAL_MS", 600000),
		DeadServiceThresholdMS: getEnvInt("DEAD_SERVICE_THRESHOLD_MS", 600000),
		WatchIntervalMS:       getEnvInt("WATCH_INTERVAL_MS", 1000),
		DebounceMS:            getEnvInt("DEBOUNCE_MS", 30000),
		EnableFileWatcher:     getEnvBool("ENABLE_FILE_WATCHER", true),
	}

	// Set mounts directory
	cfg.MountsDir = cfg.MetaCorePath + "/mounts"

	return cfg
}

// LockFilePath returns the path to the leader lock file
func (c *Config) LockFilePath() string {
	return c.MetaCorePath + "/locks/kv-leader.lock"
}

// InfoFilePath returns the path to the leader info file
func (c *Config) InfoFilePath() string {
	return c.MetaCorePath + "/locks/kv-leader.info"
}

// RedisDataDir returns the path to Redis data directory
func (c *Config) RedisDataDir() string {
	return c.MetaCorePath + "/db/redis"
}

// ServicesDir returns the path to services directory
func (c *Config) ServicesDir() string {
	return c.MetaCorePath + "/services"
}

// MountsFilePath returns the path to the mounts configuration file
func (c *Config) MountsFilePath() string {
	return c.MountsDir + "/mounts.json"
}

// MountsErrorDir returns the path to mount error files
func (c *Config) MountsErrorDir() string {
	return c.MountsDir + "/errors"
}

// WatchersFilePath returns the path to the watchers configuration file
func (c *Config) WatchersFilePath() string {
	return c.MetaCorePath + "/watchers.json"
}

// IdentityDir returns the directory holding the User Data Layer signing
// identity (created by the dashboard's Identity menu — never auto-generated
// at startup).
func (c *Config) IdentityDir() string {
	return c.MetaCorePath + "/identity"
}

// IdentityFilePath returns the path to the legacy single-identity JSON. It is
// only read once, by the multi-account migration on startup; new accounts live
// under IdentityAccountsDir.
func (c *Config) IdentityFilePath() string {
	return c.IdentityDir() + "/identity.json"
}

// IdentityAccountsDir returns the directory holding one JSON file per account
// in the multi-account keystore ({uid}.json). meta-core is a pure multi-key
// signer — display name / avatar live in the User Data Layer, not here.
func (c *Config) IdentityAccountsDir() string {
	return c.IdentityDir() + "/accounts"
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		lower := strings.ToLower(value)
		return lower == "true" || lower == "1" || lower == "yes"
	}
	return defaultValue
}

// DefaultWatchPath returns the default watch folder path
func (c *Config) DefaultWatchPath() string {
	return c.FilesPath + "/watch"
}

// DefaultWatcherPaths returns every directory that should be watched out of
// the box on a fresh install. The watcher seeds these on first start.
//
//   - /files/watch  — user-curated media. The main library.
//   - /files/plugin — plugin output (TMDB posters/backdrops, extracted
//                     subtitles, etc.). Indexed so each artefact gets a
//                     midhash256 alias and CID-based references from
//                     other files (e.g. `backdrop=<cid>`) resolve through
//                     the standard reverse index instead of needing a
//                     path companion field.
func (c *Config) DefaultWatcherPaths() []string {
	return []string{
		c.FilesPath + "/watch",
		c.FilesPath + "/plugin",
	}
}
