package watcher

import (
	"encoding/json"
	"log"
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
	// Event polling (for services that don't use Redis Streams)
	r.HandleFunc("/api/events/poll", h.handlePoll).Methods("GET")

	// Deprecated scan endpoints - redirect to /api/watchers
	r.HandleFunc("/api/scan/trigger", h.handleTriggerScanDeprecated).Methods("POST")
	r.HandleFunc("/api/scan/status", h.handleScanStatusDeprecated).Methods("GET")
}

// handlePoll handles GET /api/events/poll.
//
// DEPRECATED. The long-poll variant over file:events has been superseded by
// the SSE endpoint at /api/events/files. New consumers should use SSE. This
// handler stays for one release with a Deprecation header before removal —
// see docs/api-mediated-access.md PR A scope.
func (h *Handlers) handlePoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Link", `</api/events/files>; rel="successor-version"`)
	w.Header().Set("Sunset", "after next release")
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

	// Read events from the Redis `file:events` stream rather than the
	// in-memory buffer. The buffer is fed by the unused debouncer path,
	// so it is always empty in practice — the stream is the source of truth.
	events, err := h.dispatcher.GetRecentEvents(sinceMS, limit)
	if err != nil {
		log.Printf("[watcher.handlePoll] failed to read stream: %v", err)
		events = []FileEvent{}
	}

	writeJSON(w, http.StatusOK, EventsListResponse{
		Events: events,
		Count:  len(events),
	})
}

// handleTriggerScanDeprecated handles POST /api/scan/trigger (deprecated)
func (h *Handlers) handleTriggerScanDeprecated(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "Use POST /api/watchers/scan-all instead. This endpoint is deprecated.",
	})
}

// handleScanStatusDeprecated handles GET /api/scan/status (deprecated)
func (h *Handlers) handleScanStatusDeprecated(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "deprecated",
		"message": "Use GET /api/watchers instead. This endpoint is deprecated.",
	})
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
