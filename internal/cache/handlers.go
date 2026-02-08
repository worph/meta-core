package cache

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gorilla/mux"
)

// Handlers provides HTTP handlers for cache management
type Handlers struct {
	manager *Manager
}

// NewHandlers creates new cache handlers
func NewHandlers(manager *Manager) *Handlers {
	return &Handlers{
		manager: manager,
	}
}

// RegisterRoutes registers cache management routes
func (h *Handlers) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/api/cache/stats", h.handleStats).Methods("GET")
	router.HandleFunc("/api/cache/clear", h.handleClear).Methods("POST")
	router.HandleFunc("/api/cache/invalidate", h.handleInvalidate).Methods("DELETE")
}

// handleStats returns cache statistics
func (h *Handlers) handleStats(w http.ResponseWriter, r *http.Request) {
	stats := h.manager.Stats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleClear clears the entire cache
func (h *Handlers) handleClear(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.Clear(); err != nil {
		log.Printf("[CacheHandlers] Error clearing cache: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Cache cleared",
	})
}

// handleInvalidate invalidates a specific path
func (h *Handlers) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path query parameter required", http.StatusBadRequest)
		return
	}

	if err := h.manager.Invalidate(path); err != nil {
		log.Printf("[CacheHandlers] Error invalidating path %s: %v", path, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Path invalidated",
		"path":    path,
	})
}
