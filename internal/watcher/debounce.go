package watcher

import (
	"sync"
	"time"
)

// Debouncer delays events to ensure file stability
type Debouncer struct {
	delay    time.Duration
	pending  map[string]*PendingEvent
	mu       sync.Mutex
	callback func(FileEvent)
	stopped  bool
}

// NewDebouncer creates a new debouncer with the specified delay
func NewDebouncer(delay time.Duration) *Debouncer {
	return &Debouncer{
		delay:   delay,
		pending: make(map[string]*PendingEvent),
	}
}

// SetCallback sets the callback function for debounced events
func (d *Debouncer) SetCallback(cb func(FileEvent)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.callback = cb
}

// Add adds or updates an event in the debounce queue
func (d *Debouncer) Add(event FileEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.stopped {
		return
	}

	key := event.Path

	if existing, ok := d.pending[key]; ok {
		// Update existing pending event
		existing.LastSeen = time.Now()
		existing.Event = event // Update with latest event info

		// Reset timer
		existing.Timer.Reset(d.delay)
	} else {
		// Create new pending event
		now := time.Now()
		pending := &PendingEvent{
			Event:     event,
			FirstSeen: now,
			LastSeen:  now,
		}

		// Create timer
		pending.Timer = time.AfterFunc(d.delay, func() {
			d.flush(key)
		})

		d.pending[key] = pending
	}
}

// flush emits the event and removes it from pending
func (d *Debouncer) flush(key string) {
	d.mu.Lock()

	pending, ok := d.pending[key]
	if !ok {
		d.mu.Unlock()
		return
	}

	// Check if enough time has passed since last activity
	elapsed := time.Since(pending.LastSeen)
	if elapsed < d.delay {
		// Reset timer for remaining time
		remaining := d.delay - elapsed
		pending.Timer.Reset(remaining)
		d.mu.Unlock()
		return
	}

	// Remove from pending
	delete(d.pending, key)
	callback := d.callback
	event := pending.Event

	d.mu.Unlock()

	// Call callback outside of lock
	if callback != nil {
		callback(event)
	}
}

// Stop stops the debouncer and cancels all pending events
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stopped = true

	// Cancel all timers
	for _, pending := range d.pending {
		pending.Timer.Stop()
	}

	// Clear pending
	d.pending = make(map[string]*PendingEvent)
}

// PendingCount returns the number of pending events
func (d *Debouncer) PendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}

// FlushAll immediately flushes all pending events
func (d *Debouncer) FlushAll() {
	d.mu.Lock()

	keys := make([]string, 0, len(d.pending))
	for key := range d.pending {
		keys = append(keys, key)
	}

	d.mu.Unlock()

	for _, key := range keys {
		d.flush(key)
	}
}
