package watchers

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
)

// Handlers provides HTTP handlers for watchers management
type Handlers struct {
	manager *Manager
	poller  *Poller
}

// NewHandlers creates new watchers handlers
func NewHandlers(manager *Manager, poller *Poller) *Handlers {
	return &Handlers{
		manager: manager,
		poller:  poller,
	}
}

// RegisterRoutes registers all watchers-related routes
func (h *Handlers) RegisterRoutes(r *mux.Router) {
	r.HandleFunc("/api/watchers", h.handleList).Methods("GET")
	r.HandleFunc("/api/watchers", h.handleCreate).Methods("POST")
	r.HandleFunc("/api/watchers/scan-all", h.handleScanAll).Methods("POST")
	r.HandleFunc("/api/watchers/reset-all", h.handleResetAll).Methods("POST")
	r.HandleFunc("/api/watchers/{id}", h.handleGet).Methods("GET")
	r.HandleFunc("/api/watchers/{id}", h.handleUpdate).Methods("PUT")
	r.HandleFunc("/api/watchers/{id}", h.handleDelete).Methods("DELETE")
	r.HandleFunc("/api/watchers/{id}/scan", h.handleScan).Methods("POST")
	r.HandleFunc("/api/watchers/{id}/reset", h.handleReset).Methods("POST")
}

// handleList handles GET /api/watchers
func (h *Handlers) handleList(w http.ResponseWriter, r *http.Request) {
	statuses := h.poller.GetWatcherStatuses()

	writeJSON(w, http.StatusOK, WatchersListResponse{
		Watchers: statuses,
		Count:    len(statuses),
	})
}

// handleGet handles GET /api/watchers/{id}
func (h *Handlers) handleGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	watcher, err := h.manager.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	// Get runtime status
	status := h.poller.GetStatus(id)

	response := WatcherStatus{
		WatcherConfig: *watcher,
		Active:        status.Active,
		LastScan:      status.LastScan,
		FileCount:     status.FileCount,
		IsScanning:    status.IsScanning,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleCreate handles POST /api/watchers
func (h *Handlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateWatcherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	watcher, err := h.manager.Create(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, watcher)
}

// handleUpdate handles PUT /api/watchers/{id}
func (h *Handlers) handleUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var req UpdateWatcherRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	watcher, err := h.manager.Update(id, req)
	if err != nil {
		if err.Error() == "watcher not found: "+id {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	// Get runtime status
	status := h.poller.GetStatus(id)

	response := WatcherStatus{
		WatcherConfig: *watcher,
		Active:        status.Active,
		LastScan:      status.LastScan,
		FileCount:     status.FileCount,
		IsScanning:    status.IsScanning,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleDelete handles DELETE /api/watchers/{id}
func (h *Handlers) handleDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.manager.Delete(id); err != nil {
		if err.Error() == "watcher not found: "+id {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"message": "Watcher deleted",
	})
}

// handleScan handles POST /api/watchers/{id}/scan
func (h *Handlers) handleScan(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	fileCount, err := h.poller.TriggerScan(id)
	if err != nil {
		if err.Error() == "watcher not found: "+id {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"message":   "Scan triggered",
		"fileCount": fileCount,
	})
}

// handleScanAll handles POST /api/watchers/scan-all
func (h *Handlers) handleScanAll(w http.ResponseWriter, r *http.Request) {
	totalCount, err := h.poller.TriggerScanAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"message":    "All watchers scanned",
		"totalFiles": totalCount,
	})
}

// handleReset handles POST /api/watchers/{id}/reset
func (h *Handlers) handleReset(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	fileCount, err := h.poller.TriggerReset(id)
	if err != nil {
		if err.Error() == "watcher not found: "+id {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"message":   "Reset complete",
		"watcherId": id,
		"fileCount": fileCount,
	})
}

// handleResetAll handles POST /api/watchers/reset-all
func (h *Handlers) handleResetAll(w http.ResponseWriter, r *http.Request) {
	totalCount, err := h.poller.TriggerResetAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":     "ok",
		"message":    "All watchers reset",
		"totalFiles": totalCount,
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
