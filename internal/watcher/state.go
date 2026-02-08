package watcher

import (
	"sync"
)

// FileState tracks the known state of a file
type FileState struct {
	Size      int64
	MtimeNano int64
	MidHash   string
}

// WatcherState tracks all files for a single watcher (in-memory)
type WatcherState struct {
	mu    sync.RWMutex
	files map[string]*FileState // key: relative path
}

// NewWatcherState creates a new watcher state tracker
func NewWatcherState() *WatcherState {
	return &WatcherState{
		files: make(map[string]*FileState),
	}
}

// Get returns the file state for a given path, or nil if not tracked
func (ws *WatcherState) Get(path string) *FileState {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.files[path]
}

// Set updates or adds the file state for a given path
func (ws *WatcherState) Set(path string, state *FileState) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.files[path] = state
}

// Delete removes a file from the tracked state
func (ws *WatcherState) Delete(path string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.files, path)
}

// GetAll returns a snapshot of all tracked files
// The returned map is a copy - safe to modify
func (ws *WatcherState) GetAll() map[string]*FileState {
	ws.mu.RLock()
	defer ws.mu.RUnlock()

	snapshot := make(map[string]*FileState, len(ws.files))
	for path, state := range ws.files {
		// Copy the state to avoid race conditions
		stateCopy := *state
		snapshot[path] = &stateCopy
	}
	return snapshot
}

// Clear removes all tracked files
func (ws *WatcherState) Clear() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.files = make(map[string]*FileState)
}

// Count returns the number of tracked files
func (ws *WatcherState) Count() int {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return len(ws.files)
}

// StateRegistry manages per-watcher state
type StateRegistry struct {
	mu     sync.RWMutex
	states map[string]*WatcherState // key: watcher ID
}

// NewStateRegistry creates a new state registry
func NewStateRegistry() *StateRegistry {
	return &StateRegistry{
		states: make(map[string]*WatcherState),
	}
}

// GetOrCreate returns the state for a watcher, creating one if needed
func (sr *StateRegistry) GetOrCreate(watcherID string) *WatcherState {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	state, exists := sr.states[watcherID]
	if !exists {
		state = NewWatcherState()
		sr.states[watcherID] = state
	}
	return state
}

// Get returns the state for a watcher, or nil if not exists
func (sr *StateRegistry) Get(watcherID string) *WatcherState {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return sr.states[watcherID]
}

// Clear removes the state for a watcher
func (sr *StateRegistry) Clear(watcherID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if state, exists := sr.states[watcherID]; exists {
		state.Clear()
	}
}

// ClearAll removes all watcher states
func (sr *StateRegistry) ClearAll() {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	for _, state := range sr.states {
		state.Clear()
	}
}

// Delete removes a watcher's state completely
func (sr *StateRegistry) Delete(watcherID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.states, watcherID)
}

// Count returns the number of files tracked for a watcher
func (sr *StateRegistry) Count(watcherID string) int {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if state, exists := sr.states[watcherID]; exists {
		return state.Count()
	}
	return 0
}
