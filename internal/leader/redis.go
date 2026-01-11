package leader

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/metazla/meta-core/internal/config"
	"github.com/redis/go-redis/v9"
)

// RedisManager handles spawning and managing the Redis process
type RedisManager struct {
	config  *config.Config
	cmd     *exec.Cmd
	running bool
	mu      sync.RWMutex
}

// NewRedisManager creates a new Redis manager
func NewRedisManager(cfg *config.Config) *RedisManager {
	return &RedisManager{
		config: cfg,
	}
}

// Start spawns the Redis server process
func (rm *RedisManager) Start() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.running {
		return nil
	}

	// Ensure Redis data directory exists
	dataDir := rm.config.RedisDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create Redis data directory: %w", err)
	}

	log.Printf("[Redis] Spawning Redis on port %d...", rm.config.RedisPort)

	// Build Redis arguments matching TypeScript implementation
	args := []string{
		"--port", fmt.Sprintf("%d", rm.config.RedisPort),
		"--bind", "0.0.0.0",
		"--dir", dataDir,
		"--appendonly", "yes",
		"--appendfilename", "appendonly.aof",
		"--dbfilename", "dump.rdb",
		"--save", "60", "1", // Save after 60 seconds if at least 1 key changed
		"--loglevel", "warning",
	}

	rm.cmd = exec.Command("redis-server", args...)
	rm.cmd.Stdout = os.Stdout
	rm.cmd.Stderr = os.Stderr

	if err := rm.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Redis: %w", err)
	}

	rm.running = true
	log.Printf("[Redis] Started with PID %d", rm.cmd.Process.Pid)

	// Monitor the process in background
	go rm.monitorProcess()

	return nil
}

// Stop gracefully stops the Redis server
func (rm *RedisManager) Stop() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if !rm.running || rm.cmd == nil || rm.cmd.Process == nil {
		return nil
	}

	log.Println("[Redis] Stopping Redis...")

	// Try graceful shutdown first (SIGTERM)
	if err := rm.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		log.Printf("[Redis] Failed to send SIGTERM: %v", err)
	}

	// Wait up to 10 seconds for graceful shutdown
	done := make(chan error, 1)
	go func() {
		done <- rm.cmd.Wait()
	}()

	select {
	case <-done:
		log.Println("[Redis] Stopped gracefully")
	case <-time.After(10 * time.Second):
		log.Println("[Redis] Timeout, sending SIGKILL...")
		rm.cmd.Process.Kill()
		<-done
	}

	rm.running = false
	rm.cmd = nil
	return nil
}

// IsRunning returns true if Redis is currently running
func (rm *RedisManager) IsRunning() bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.running
}

// WaitForReady waits for Redis to be ready to accept connections
func (rm *RedisManager) WaitForReady(timeout time.Duration) error {
	log.Println("[Redis] Waiting for Redis to be ready...")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("localhost:%d", rm.config.RedisPort),
	})
	defer client.Close()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for Redis")
		case <-ticker.C:
			if err := client.Ping(ctx).Err(); err == nil {
				log.Println("[Redis] Ready to accept connections")
				return nil
			}
		}
	}
}

// Ping checks if Redis is responding
func (rm *RedisManager) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("localhost:%d", rm.config.RedisPort),
	})
	defer client.Close()

	return client.Ping(ctx).Err()
}

// monitorProcess monitors the Redis process and updates state when it exits
func (rm *RedisManager) monitorProcess() {
	if rm.cmd == nil {
		return
	}

	err := rm.cmd.Wait()

	rm.mu.Lock()
	rm.running = false
	rm.mu.Unlock()

	if err != nil {
		log.Printf("[Redis] Process exited with error: %v", err)
	} else {
		log.Println("[Redis] Process exited normally")
	}
}
