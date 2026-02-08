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

	// Redis configuration
	RedisPort int // Redis port (default: 6379)

	// HTTP API configuration
	HTTPPort int    // HTTP API port (default: 9000)
	HTTPHost string // HTTP API host (default: "127.0.0.1")

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

	// Cache configuration
	CacheEnabled    bool   // Enable WebDAV caching (default: true)
	CachePath       string // Path to cache directory (default: /meta-core/cache)
	CacheMaxSizeGB  int    // Maximum cache size in GB (default: 100)
	CacheTTLSeconds int    // Cache entry TTL in seconds (default: 3600)
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
		RedisPort:             getEnvInt("REDIS_PORT", 6379),
		HTTPPort:              getEnvInt("META_CORE_HTTP_PORT", 9000),
		HTTPHost:              getEnv("META_CORE_HTTP_HOST", "127.0.0.1"),
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

	// Set cache configuration
	cfg.CacheEnabled = getEnvBool("CACHE_ENABLED", true)
	cfg.CachePath = getEnv("CACHE_PATH", cfg.MetaCorePath+"/cache")
	cfg.CacheMaxSizeGB = getEnvInt("CACHE_MAX_SIZE_GB", 100)
	cfg.CacheTTLSeconds = getEnvInt("CACHE_TTL_SECONDS", 3600)

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

// CacheDir returns the path to the cache directory
func (c *Config) CacheDir() string {
	return c.CachePath
}

// CacheIndexPath returns the path to the cache index file
func (c *Config) CacheIndexPath() string {
	return c.CachePath + "/cache-index.json"
}

// WatchersFilePath returns the path to the watchers configuration file
func (c *Config) WatchersFilePath() string {
	return c.MetaCorePath + "/watchers.json"
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
