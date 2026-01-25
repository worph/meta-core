package watcher

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	// MaxRetries is the maximum number of delivery retries
	MaxRetries = 3
	// RetryDelay is the delay between retries
	RetryDelay = 5 * time.Second
	// WebhookTimeout is the timeout for webhook requests
	WebhookTimeout = 10 * time.Second
	// MaxFailCount is the max failures before removing subscriber
	MaxFailCount = 10
)

// Dispatcher sends file events to subscribers
type Dispatcher struct {
	subscribers map[string]*Subscriber
	sseClients  map[chan FileEvent]bool
	mu          sync.RWMutex
	httpClient  *http.Client
}

// NewDispatcher creates a new event dispatcher
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		subscribers: make(map[string]*Subscriber),
		sseClients:  make(map[chan FileEvent]bool),
		httpClient: &http.Client{
			Timeout: WebhookTimeout,
		},
	}
}

// Subscribe registers a webhook subscriber
func (d *Dispatcher) Subscribe(url string, eventTypes []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.subscribers[url] = &Subscriber{
		URL:          url,
		RegisteredAt: NowMS(),
		EventTypes:   eventTypes,
		FailCount:    0,
	}

	log.Printf("[Dispatcher] Subscribed webhook: %s", url)
	return nil
}

// Unsubscribe removes a webhook subscriber
func (d *Dispatcher) Unsubscribe(url string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.subscribers, url)
	log.Printf("[Dispatcher] Unsubscribed webhook: %s", url)
	return nil
}

// ListSubscribers returns all subscribers
func (d *Dispatcher) ListSubscribers() []Subscriber {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]Subscriber, 0, len(d.subscribers))
	for _, sub := range d.subscribers {
		result = append(result, *sub)
	}
	return result
}

// AddSSEClient adds an SSE client channel
func (d *Dispatcher) AddSSEClient(ch chan FileEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sseClients[ch] = true
}

// RemoveSSEClient removes an SSE client channel
func (d *Dispatcher) RemoveSSEClient(ch chan FileEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sseClients, ch)
	close(ch)
}

// Dispatch sends an event to all subscribers
func (d *Dispatcher) Dispatch(event FileEvent) {
	// Dispatch to webhooks
	go d.dispatchToWebhooks(event)

	// Dispatch to SSE clients
	d.dispatchToSSE(event)
}

// dispatchToWebhooks sends event to webhook subscribers
func (d *Dispatcher) dispatchToWebhooks(event FileEvent) {
	d.mu.RLock()
	subscribers := make([]*Subscriber, 0, len(d.subscribers))
	for _, sub := range d.subscribers {
		subscribers = append(subscribers, sub)
	}
	d.mu.RUnlock()

	for _, sub := range subscribers {
		// Check event type filter
		if len(sub.EventTypes) > 0 {
			found := false
			for _, et := range sub.EventTypes {
				if et == string(event.Type) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		go d.deliverToWebhook(sub.URL, event)
	}
}

// deliverToWebhook delivers an event to a webhook with retries
func (d *Dispatcher) deliverToWebhook(url string, event FileEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		log.Printf("[Dispatcher] Failed to marshal event: %v", err)
		return
	}

	var lastErr error
	for attempt := 0; attempt < MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(RetryDelay)
		}

		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Event-Type", string(event.Type))

		resp, err := d.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success
			d.mu.Lock()
			if sub, ok := d.subscribers[url]; ok {
				sub.LastDelivery = NowMS()
				sub.FailCount = 0
			}
			d.mu.Unlock()
			return
		}

		lastErr = &httpError{StatusCode: resp.StatusCode}
	}

	// All retries failed
	log.Printf("[Dispatcher] Failed to deliver to %s after %d retries: %v", url, MaxRetries, lastErr)

	d.mu.Lock()
	if sub, ok := d.subscribers[url]; ok {
		sub.FailCount++
		if sub.FailCount >= MaxFailCount {
			delete(d.subscribers, url)
			log.Printf("[Dispatcher] Removed webhook %s after %d failures", url, MaxFailCount)
		}
	}
	d.mu.Unlock()
}

// dispatchToSSE sends event to SSE clients
func (d *Dispatcher) dispatchToSSE(event FileEvent) {
	d.mu.RLock()
	clients := make([]chan FileEvent, 0, len(d.sseClients))
	for ch := range d.sseClients {
		clients = append(clients, ch)
	}
	d.mu.RUnlock()

	for _, ch := range clients {
		select {
		case ch <- event:
		default:
			// Channel full, skip
		}
	}
}

// SSEClientCount returns the number of connected SSE clients
func (d *Dispatcher) SSEClientCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.sseClients)
}

// httpError represents an HTTP error
type httpError struct {
	StatusCode int
}

func (e *httpError) Error() string {
	return http.StatusText(e.StatusCode)
}
