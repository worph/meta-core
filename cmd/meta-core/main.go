package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/metazla/meta-core/internal/api"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/discovery"
	"github.com/metazla/meta-core/internal/leader"
	"github.com/metazla/meta-core/internal/meshdisco"
	"github.com/metazla/meta-core/internal/storage"
)

// Version is set at build time
var Version = "1.0.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("[meta-core] Starting version %s", Version)

	// Load configuration
	cfg := config.Load()
	log.Printf("[meta-core] Service: %s, HTTP Port: %d", cfg.ServiceName, cfg.HTTPPort)

	// Create leader info provider (Go binary only runs as leader - followers loop in bash)
	leaderProvider := leader.NewLeaderInfoProvider(cfg)

	// Connect to local Redis (supervisord starts Redis before meta-core)
	// Retry connection since Redis may still be loading its dataset
	redisURL := fmt.Sprintf("redis://localhost:%d", cfg.RedisPort)
	log.Printf("[meta-core] Connecting to Redis at %s", redisURL)

	storageClient := storage.NewClient("")
	maxRetries := 30
	retryDelay := 1 // seconds
	var err error
	for i := 0; i < maxRetries; i++ {
		err = storageClient.Connect(redisURL)
		if err == nil {
			break
		}
		log.Printf("[meta-core] Redis connection attempt %d/%d failed: %v", i+1, maxRetries, err)
		time.Sleep(time.Duration(retryDelay) * time.Second)
	}
	if err != nil {
		log.Fatalf("[meta-core] Failed to connect to Redis after %d attempts: %v", maxRetries, err)
	}
	log.Println("[meta-core] Connected to Redis")

	// Schema-version sentinel: alpha clean-wipe gate. Refuses to boot when
	// the existing Redis store predates the UUID-rooted schema, so legacy
	// data can't silently leak into the new code paths. See
	// docs/uuid-rooted-metadata.md.
	if err := storageClient.EnsureSchemaVersion(storage.SchemaVersion); err != nil {
		log.Fatalf("[meta-core] %v", err)
	}
	log.Printf("[meta-core] Redis schema version OK (v%d)", storage.SchemaVersion)

	// Create and start service discovery
	disc := discovery.NewService(cfg)
	if err := disc.Start(); err != nil {
		log.Fatalf("[meta-core] Failed to start service discovery: %v", err)
	}

	// Create and start dead service cleaner
	cleaner := discovery.NewCleaner(cfg)
	if err := cleaner.Start(); err != nil {
		log.Printf("[meta-core] Warning: failed to start service cleaner: %v", err)
	}

	// UDP service discovery (meta-discovery v1). Runs alongside the file
	// registry above for now — nothing consumes the wire yet, so this cannot
	// regress the existing path. The announce payload is rebuilt from
	// leaderProvider on every tick, which is what keeps it and GET /urls from
	// drifting apart.
	var mesh *meshdisco.Node
	if cfg.EnableUDPDiscovery {
		hostname, _ := os.Hostname()
		mesh = meshdisco.New(meshdisco.Config{
			Name:     cfg.ServiceName,
			Instance: hostname,
			Role:     meshdisco.RoleCore,
			Version:  Version,
			Group:    cfg.DiscoveryGroup,
			Port:     cfg.DiscoveryPort,
			Interval: time.Duration(cfg.DiscoveryIntervalMS) * time.Millisecond,
			Payload: func() (string, string, *meshdisco.URLs) {
				info := leaderProvider.LeaderInfo()
				if info == nil {
					return "", "starting", nil
				}
				return coreBaseURL(cfg), "running", &meshdisco.URLs{
					Hostname:          info.Hostname,
					BaseUrl:           info.BaseUrl,
					ApiUrl:            info.ApiUrl,
					WebdavUrl:         info.WebdavUrl,
					WebdavUrlInternal: info.WebdavUrlInternal,
				}
			},
		})
		if err := mesh.Start(); err != nil {
			log.Printf("[meta-core] Warning: UDP discovery disabled: %v", err)
			mesh = nil
		}
	} else {
		log.Println("[meta-core] UDP discovery disabled by ENABLE_UDP_DISCOVERY")
	}

	// Create and start API server
	apiServer := api.NewServer(cfg, leaderProvider, disc, cleaner, storageClient)
	apiServer.SetMeshNode(mesh)
	if err := apiServer.Start(); err != nil {
		log.Fatalf("[meta-core] Failed to start API server: %v", err)
	}

	log.Println("[meta-core] Ready and serving requests")

	// Wait for shutdown signal
	waitForShutdown()

	log.Println("[meta-core] Shutting down...")

	// Graceful shutdown in reverse order
	if err := apiServer.Stop(); err != nil {
		log.Printf("[meta-core] Error stopping API server: %v", err)
	}

	if mesh != nil {
		if err := mesh.Stop(); err != nil {
			log.Printf("[meta-core] Error stopping UDP discovery: %v", err)
		}
	}

	if err := cleaner.Stop(); err != nil {
		log.Printf("[meta-core] Error stopping service cleaner: %v", err)
	}

	if err := disc.Stop(); err != nil {
		log.Printf("[meta-core] Error stopping service discovery: %v", err)
	}

	if err := storageClient.Close(); err != nil {
		log.Printf("[meta-core] Error closing storage client: %v", err)
	}

	log.Println("[meta-core] Shutdown complete")
}

// waitForShutdown blocks until a shutdown signal is received
func waitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
}

// coreBaseURL is the browser-facing URL announced for the nav menu. It keeps
// the three-way fallback the file registry used (discovery/service.go):
// META_CORE_PUBLIC_URL wins (the reachable URL when there is no Caddy
// perimeter, e.g. a debug-direct port), then BASE_URL (the Caddy URL), then
// the container IP.
func coreBaseURL(cfg *config.Config) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	return fmt.Sprintf("http://%s:%d", meshdisco.LocalIPv4(), cfg.APIPort)
}
