package main

import (
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

	// Create storage client
	storageClient := storage.NewClient("")

	// Create leader election
	election := leader.NewElection(cfg)

	// Wire up storage connector to election
	election.SetStorageConnector(&storageConnectorAdapter{client: storageClient})

	// Set up leader/follower callbacks
	election.OnBecomeLeader(func() {
		log.Println("[meta-core] Became LEADER - Redis is running")
	})

	election.OnBecomeFollower(func(info *leader.LeaderLockInfo) {
		log.Printf("[meta-core] Became FOLLOWER - Leader at %s", info.API)
	})

	election.OnLeaderLost(func() {
		log.Println("[meta-core] Lost leadership")
	})

	// Start leader election
	if err := election.Start(); err != nil {
		log.Fatalf("[meta-core] Failed to start leader election: %v", err)
	}

	// Create and start service discovery
	disc := discovery.NewService(cfg)
	if err := disc.Start(); err != nil {
		log.Fatalf("[meta-core] Failed to start service discovery: %v", err)
	}

	// Create and start API server
	apiServer := api.NewServer(cfg, election, disc, storageClient)
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

	if err := disc.Stop(); err != nil {
		log.Printf("[meta-core] Error stopping service discovery: %v", err)
	}

	if err := election.Stop(); err != nil {
		log.Printf("[meta-core] Error stopping leader election: %v", err)
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

// storageConnectorAdapter adapts storage.Client to leader.StorageConnector
type storageConnectorAdapter struct {
	client *storage.Client
}

func (a *storageConnectorAdapter) Connect(url string) error {
	return a.client.Connect(url)
}

func (a *storageConnectorAdapter) Close() error {
	return a.client.Close()
}
