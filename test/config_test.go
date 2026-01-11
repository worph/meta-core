package test

import (
	"os"
	"testing"

	"github.com/metazla/meta-core/internal/config"
)

func TestConfigDefaults(t *testing.T) {
	// Clear environment
	os.Clearenv()

	cfg := config.Load()

	// Check defaults
	if cfg.MetaCorePath != "/meta-core" {
		t.Errorf("Expected MetaCorePath '/meta-core', got '%s'", cfg.MetaCorePath)
	}

	if cfg.FilesPath != "/files" {
		t.Errorf("Expected FilesPath '/files', got '%s'", cfg.FilesPath)
	}

	if cfg.ServiceName != "meta-core" {
		t.Errorf("Expected ServiceName 'meta-core', got '%s'", cfg.ServiceName)
	}

	if cfg.HTTPPort != 9000 {
		t.Errorf("Expected HTTPPort 9000, got %d", cfg.HTTPPort)
	}

	if cfg.RedisPort != 6379 {
		t.Errorf("Expected RedisPort 6379, got %d", cfg.RedisPort)
	}
}

func TestConfigFromEnv(t *testing.T) {
	// Set environment variables
	os.Setenv("META_CORE_PATH", "/custom/meta-core")
	os.Setenv("FILES_PATH", "/custom/files")
	os.Setenv("SERVICE_NAME", "test-service")
	os.Setenv("META_CORE_HTTP_PORT", "8080")
	os.Setenv("REDIS_PORT", "6380")
	defer os.Clearenv()

	cfg := config.Load()

	if cfg.MetaCorePath != "/custom/meta-core" {
		t.Errorf("Expected MetaCorePath '/custom/meta-core', got '%s'", cfg.MetaCorePath)
	}

	if cfg.FilesPath != "/custom/files" {
		t.Errorf("Expected FilesPath '/custom/files', got '%s'", cfg.FilesPath)
	}

	if cfg.ServiceName != "test-service" {
		t.Errorf("Expected ServiceName 'test-service', got '%s'", cfg.ServiceName)
	}

	if cfg.HTTPPort != 8080 {
		t.Errorf("Expected HTTPPort 8080, got %d", cfg.HTTPPort)
	}

	if cfg.RedisPort != 6380 {
		t.Errorf("Expected RedisPort 6380, got %d", cfg.RedisPort)
	}
}

func TestConfigPaths(t *testing.T) {
	os.Setenv("META_CORE_PATH", "/test")
	defer os.Clearenv()

	cfg := config.Load()

	if cfg.LockFilePath() != "/test/locks/kv-leader.lock" {
		t.Errorf("Unexpected LockFilePath: %s", cfg.LockFilePath())
	}

	if cfg.InfoFilePath() != "/test/locks/kv-leader.info" {
		t.Errorf("Unexpected InfoFilePath: %s", cfg.InfoFilePath())
	}

	if cfg.RedisDataDir() != "/test/db/redis" {
		t.Errorf("Unexpected RedisDataDir: %s", cfg.RedisDataDir())
	}

	if cfg.ServicesDir() != "/test/services" {
		t.Errorf("Unexpected ServicesDir: %s", cfg.ServicesDir())
	}
}
