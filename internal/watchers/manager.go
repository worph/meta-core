package watchers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
	"github.com/metazla/meta-core/internal/config"
)

// Manager handles CRUD operations for watchers configuration
type Manager struct {
	config       *config.Config
	watchersFile string

	mu       sync.RWMutex
	watchers map[string]*WatcherConfig

	// Callback to notify poller of changes
	onChanged func()
}

// NewManager creates a new watchers manager
func NewManager(cfg *config.Config) (*Manager, error) {
	watchersFile := cfg.WatchersFilePath()

	m := &Manager{
		config:       cfg,
		watchersFile: watchersFile,
		watchers:     make(map[string]*WatcherConfig),
	}

	// Ensure directory exists
	dir := filepath.Dir(watchersFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create watchers directory: %w", err)
	}

	// Load existing or create default
	if err := m.loadOrCreateDefault(); err != nil {
		return nil, err
	}

	return m, nil
}

// SetOnChanged sets the callback for watcher changes
func (m *Manager) SetOnChanged(cb func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChanged = cb
}

// loadOrCreateDefault loads existing config or creates default watcher
func (m *Manager) loadOrCreateDefault() error {
	// Try to load existing file
	if _, err := os.Stat(m.watchersFile); err == nil {
		return m.load()
	}

	// File doesn't exist, create default watcher for /files/watch
	log.Printf("[Watchers] No existing config, creating default watcher for %s", m.config.DefaultWatchPath())
	return m.createDefaultWatcher()
}

// createDefaultWatcher creates the default /files/watch watcher and folder
func (m *Manager) createDefaultWatcher() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	defaultPath := m.config.DefaultWatchPath()

	// Create the watch folder if it doesn't exist
	if err := os.MkdirAll(defaultPath, 0755); err != nil {
		log.Printf("[Watchers] Warning: failed to create default watch folder %s: %v", defaultPath, err)
		// Continue anyway - folder might be created later or mounted
	} else {
		log.Printf("[Watchers] Created default watch folder: %s", defaultPath)
	}

	// Create default watcher
	id := uuid.New().String()[:8]
	watcher := &WatcherConfig{
		ID:         id,
		Path:       defaultPath,
		IntervalMs: DefaultIntervalMs,
		Enabled:    true,
		CreatedAt:  NowMS(),
	}
	m.watchers[id] = watcher
	log.Printf("[Watchers] Created default watcher: %s -> %s", id, defaultPath)

	// Save config
	return m.saveUnsafe()
}

// load loads watchers from file
func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.watchersFile)
	if err != nil {
		return fmt.Errorf("failed to read watchers file: %w", err)
	}

	var file WatchersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("failed to parse watchers file: %w", err)
	}

	m.watchers = make(map[string]*WatcherConfig)
	for i := range file.Watchers {
		w := file.Watchers[i]
		m.watchers[w.ID] = &w
	}

	log.Printf("[Watchers] Loaded %d watchers from file", len(m.watchers))
	return nil
}

// save persists watchers to file (thread-safe)
func (m *Manager) save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveUnsafe()
}

// saveUnsafe persists watchers to file (must hold lock)
func (m *Manager) saveUnsafe() error {
	watchers := make([]WatcherConfig, 0, len(m.watchers))
	for _, w := range m.watchers {
		watchers = append(watchers, *w)
	}

	file := WatchersFile{
		Version:  1,
		Watchers: watchers,
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal watchers: %w", err)
	}

	if err := os.WriteFile(m.watchersFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write watchers file: %w", err)
	}

	return nil
}

// notifyChanged calls the onChanged callback if set
func (m *Manager) notifyChanged() {
	m.mu.RLock()
	cb := m.onChanged
	m.mu.RUnlock()

	if cb != nil {
		go cb()
	}
}

// List returns all watchers
func (m *Manager) List() []WatcherConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]WatcherConfig, 0, len(m.watchers))
	for _, w := range m.watchers {
		result = append(result, *w)
	}
	return result
}

// Get returns a watcher by ID
func (m *Manager) Get(id string) (*WatcherConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	w, ok := m.watchers[id]
	if !ok {
		return nil, fmt.Errorf("watcher not found: %s", id)
	}
	return &WatcherConfig{
		ID:         w.ID,
		Path:       w.Path,
		IntervalMs: w.IntervalMs,
		Enabled:    w.Enabled,
		CreatedAt:  w.CreatedAt,
	}, nil
}

// Create adds a new watcher
func (m *Manager) Create(req CreateWatcherRequest) (*WatcherConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate path
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Set defaults
	intervalMs := req.IntervalMs
	if intervalMs < MinIntervalMs {
		intervalMs = DefaultIntervalMs
	}

	id := uuid.New().String()[:8]
	watcher := &WatcherConfig{
		ID:         id,
		Path:       req.Path,
		IntervalMs: intervalMs,
		Enabled:    req.Enabled,
		CreatedAt:  NowMS(),
	}

	m.watchers[id] = watcher

	if err := m.saveUnsafe(); err != nil {
		delete(m.watchers, id)
		return nil, err
	}

	log.Printf("[Watchers] Created watcher: %s -> %s", id, req.Path)

	// Notify after releasing lock
	go m.notifyChanged()

	return &WatcherConfig{
		ID:         watcher.ID,
		Path:       watcher.Path,
		IntervalMs: watcher.IntervalMs,
		Enabled:    watcher.Enabled,
		CreatedAt:  watcher.CreatedAt,
	}, nil
}

// Update modifies an existing watcher
func (m *Manager) Update(id string, req UpdateWatcherRequest) (*WatcherConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w, ok := m.watchers[id]
	if !ok {
		return nil, fmt.Errorf("watcher not found: %s", id)
	}

	if req.Path != nil {
		if *req.Path == "" {
			return nil, fmt.Errorf("path cannot be empty")
		}
		w.Path = *req.Path
	}

	if req.IntervalMs != nil {
		interval := *req.IntervalMs
		if interval < MinIntervalMs {
			interval = MinIntervalMs
		}
		w.IntervalMs = interval
	}

	if req.Enabled != nil {
		w.Enabled = *req.Enabled
	}

	if err := m.saveUnsafe(); err != nil {
		return nil, err
	}

	log.Printf("[Watchers] Updated watcher: %s", id)

	// Notify after releasing lock
	go m.notifyChanged()

	return &WatcherConfig{
		ID:         w.ID,
		Path:       w.Path,
		IntervalMs: w.IntervalMs,
		Enabled:    w.Enabled,
		CreatedAt:  w.CreatedAt,
	}, nil
}

// Delete removes a watcher
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.watchers[id]; !ok {
		return fmt.Errorf("watcher not found: %s", id)
	}

	delete(m.watchers, id)

	if err := m.saveUnsafe(); err != nil {
		return err
	}

	log.Printf("[Watchers] Deleted watcher: %s", id)

	// Notify after releasing lock
	go m.notifyChanged()

	return nil
}

// GetFilesPath returns the files path from config
func (m *Manager) GetFilesPath() string {
	return m.config.FilesPath
}
