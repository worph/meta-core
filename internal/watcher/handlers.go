package watcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
)

// Handlers provides HTTP handlers for watcher operations
type Handlers struct {
	watcher    *Watcher
	dispatcher *Dispatcher
}

// NewHandlers creates new watcher handlers
func NewHandlers(watcher *Watcher, dispatcher *Dispatcher) *Handlers {
	return &Handlers{
		watcher:    watcher,
		dispatcher: dispatcher,
	}
}

// RegisterRoutes registers all watcher-related routes
func (h *Handlers) RegisterRoutes(r *mux.Router) {
	// Event subscriptions
	r.HandleFunc("/api/events/subscribe", h.handleSSESubscribe).Methods("GET")
	r.HandleFunc("/api/events/poll", h.handlePoll).Methods("GET")
	r.HandleFunc("/api/events/subscribers", h.handleListSubscribers).Methods("GET")
	r.HandleFunc("/api/events/subscribers", h.handleAddSubscriber).Methods("POST")
	r.HandleFunc("/api/events/subscribers/{url}", h.handleRemoveSubscriber).Methods("DELETE")

	// Scan management
	r.HandleFunc("/api/scan/trigger", h.handleTriggerScan).Methods("POST")
	r.HandleFunc("/api/scan/status", h.handleScanStatus).Methods("GET")
}

// handleSSESubscribe handles GET /api/events/subscribe (Server-Sent Events)
func (h *Handlers) handleSSESubscribe(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create event channel
	eventChan := make(chan FileEvent, 100)
	h.dispatcher.AddSSEClient(eventChan)

	// Cleanup on disconnect
	defer h.dispatcher.RemoveSSEClient(eventChan)

	// Get flusher
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Send initial connected message
	fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
	flusher.Flush()

	// Stream events
	for {
		select {
		case event := <-eventChan:
			data, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: file\ndata: %s\n\n", string(data))
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}

// handlePoll handles GET /api/events/poll
func (h *Handlers) handlePoll(w http.ResponseWriter, r *http.Request) {
	// Get since parameter
	sinceStr := r.URL.Query().Get("since")
	sinceMS := int64(0)
	if sinceStr != "" {
		if parsed, err := strconv.ParseInt(sinceStr, 10, 64); err == nil {
			sinceMS = parsed
		}
	}

	// Get limit parameter
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}

	events := h.watcher.GetRecentEvents(sinceMS, limit)

	writeJSON(w, http.StatusOK, EventsListResponse{
		Events: events,
		Count:  len(events),
	})
}

// handleListSubscribers handles GET /api/events/subscribers
func (h *Handlers) handleListSubscribers(w http.ResponseWriter, r *http.Request) {
	subscribers := h.dispatcher.ListSubscribers()

	writeJSON(w, http.StatusOK, SubscribersListResponse{
		Subscribers: subscribers,
		Count:       len(subscribers),
	})
}

// handleAddSubscriber handles POST /api/events/subscribers
func (h *Handlers) handleAddSubscriber(w http.ResponseWriter, r *http.Request) {
	var req SubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "URL is required")
		return
	}

	if err := h.dispatcher.Subscribe(req.URL, req.EventTypes); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"status":  "ok",
		"message": "Subscribed",
		"url":     req.URL,
	})
}

// handleRemoveSubscriber handles DELETE /api/events/subscribers/{url}
func (h *Handlers) handleRemoveSubscriber(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	url := vars["url"]

	if url == "" {
		writeError(w, http.StatusBadRequest, "URL is required")
		return
	}

	if err := h.dispatcher.Unsubscribe(url); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "Unsubscribed",
	})
}

// handleTriggerScan handles POST /api/scan/trigger
func (h *Handlers) handleTriggerScan(w http.ResponseWriter, r *http.Request) {
	go h.watcher.RunScan()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "Scan triggered",
	})
}

// handleScanStatus handles GET /api/scan/status
func (h *Handlers) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	status := h.watcher.GetStatus()
	writeJSON(w, http.StatusOK, status)
}

// Helper functions

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{
		"error":   http.StatusText(status),
		"message": message,
	})
}
