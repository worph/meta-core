package watcher

import (
	"encoding/json"
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
