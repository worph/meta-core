package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/metazla/meta-core/internal/api"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/discovery"
	"github.com/metazla/meta-core/internal/leader"
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

	// Create role provider (reads META_CORE_ROLE from environment, set by leader-election.sh)
	roleProvider := leader.NewLocalRoleProvider(cfg)
	log.Printf("[meta-core] Role: %s", roleProvider.Role())

	// Connect to local Redis (supervisord starts Redis before meta-core)
	redisURL := fmt.Sprintf("redis://localhost:%d", cfg.RedisPort)
	log.Printf("[meta-core] Connecting to Redis at %s", redisURL)

	storageClient := storage.NewClient("")
	if err := storageClient.Connect(redisURL); err != nil {
		log.Fatalf("[meta-core] Failed to connect to Redis: %v", err)
	}
	log.Println("[meta-core] Connected to Redis")

	// Create and start service discovery
	disc := discovery.NewService(cfg)
	disc.SetRoleProvider(&roleProviderAdapter{provider: roleProvider})
	if err := disc.Start(); err != nil {
		log.Fatalf("[meta-core] Failed to start service discovery: %v", err)
	}

	// Create and start dead service cleaner
	cleaner := discovery.NewCleaner(cfg)
	if err := cleaner.Start(); err != nil {
		log.Printf("[meta-core] Warning: failed to start service cleaner: %v", err)
	}

	// Create and start API server
	apiServer := api.NewServer(cfg, roleProvider, disc, cleaner, storageClient)
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

// roleProviderAdapter adapts leader.RoleProvider to discovery.RoleProvider
type roleProviderAdapter struct {
	provider leader.RoleProvider
}

func (a *roleProviderAdapter) Role() string {
	return string(a.provider.Role())
}
