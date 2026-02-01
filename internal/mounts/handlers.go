package mounts

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
)

// Handlers provides HTTP handlers for mount operations
type Handlers struct {
	manager  *Manager
	scanFunc ScanFunc
}

// NewHandlers creates new mount handlers
func NewHandlers(manager *Manager, scanFunc ScanFunc) *Handlers {
	return &Handlers{
		manager:  manager,
		scanFunc: scanFunc,
	}
}

// RegisterRoutes registers all mount-related routes
func (h *Handlers) RegisterRoutes(r *mux.Router) {
	// Mount management
	r.HandleFunc("/api/mounts", h.handleListMounts).Methods("GET")
	r.HandleFunc("/api/mounts", h.handleCreateMount).Methods("POST")
	r.HandleFunc("/api/mounts/rclone/remotes", h.handleListRcloneRemotes).Methods("GET")
	r.HandleFunc("/api/mounts/{id}", h.handleGetMount).Methods("GET")
	r.HandleFunc("/api/mounts/{id}", h.handleUpdateMount).Methods("PUT", "PATCH")
	r.HandleFunc("/api/mounts/{id}", h.handleDeleteMount).Methods("DELETE")
	r.HandleFunc("/api/mounts/{id}/mount", h.handleRequestMount).Methods("POST")
	r.HandleFunc("/api/mounts/{id}/unmount", h.handleRequestUnmount).Methods("POST")
	r.HandleFunc("/api/mounts/{id}/safe-unmount", h.handleSafeUnmount).Methods("POST")
	r.HandleFunc("/api/mounts/{id}/scan", h.handleTriggerMountScan).Methods("POST")
}

// handleListMounts handles GET /api/mounts
func (h *Handlers) handleListMounts(w http.ResponseWriter, r *http.Request) {
	mounts, err := h.manager.ListMounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, MountsListResponse{Mounts: mounts})
}

// handleGetMount handles GET /api/mounts/{id}
func (h *Handlers) handleGetMount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	mount, err := h.manager.GetMount(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if mount == nil {
		writeError(w, http.StatusNotFound, "mount not found")
		return
	}

	writeJSON(w, http.StatusOK, MountResponse{Mount: mount})
}

// handleCreateMount handles POST /api/mounts
func (h *Handlers) handleCreateMount(w http.ResponseWriter, r *http.Request) {
	var req CreateMountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	mount, err := h.manager.CreateMount(&req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, MountResponse{Mount: mount})
}

// handleDeleteMount handles DELETE /api/mounts/{id}
func (h *Handlers) handleDeleteMount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.manager.DeleteMount(id); err != nil {
		if err.Error() == "mount not found" {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, StatusResponse{Status: "ok"})
}

// handleRequestMount handles POST /api/mounts/{id}/mount
func (h *Handlers) handleRequestMount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.manager.RequestMount(id); err != nil {
		if err.Error() == "mount not found" {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:  "ok",
		Message: "Mount requested",
	})
}

// handleRequestUnmount handles POST /api/mounts/{id}/unmount
func (h *Handlers) handleRequestUnmount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	if err := h.manager.RequestUnmount(id); err != nil {
		if err.Error() == "mount not found" {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:  "ok",
		Message: "Unmount requested",
	})
}

// handleSafeUnmount handles POST /api/mounts/{id}/safe-unmount
func (h *Handlers) handleSafeUnmount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// Get timeout from query param (default 60000ms)
	timeoutStr := r.URL.Query().Get("timeout")
	timeoutMS := 60000
	if timeoutStr != "" {
		if parsed, err := parseIntOrDefault(timeoutStr, 60000); err == nil {
			timeoutMS = parsed
		}
	}

	// Get mount info
	mount, err := h.manager.GetMount(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if mount == nil {
		writeError(w, http.StatusNotFound, "mount not found")
		return
	}

	// Request unmount
	if err := h.manager.RequestUnmount(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Wait for unmount
	success := h.manager.WaitForUnmount(mount.MountPath, timeoutMS)

	if !success {
		writeJSON(w, http.StatusOK, StatusResponse{
			Status:  "warning",
			Message: "Unmount requested but mount may still be attached",
		})
		return
	}

	writeJSON(w, http.StatusOK, StatusResponse{
		Status:  "ok",
		Message: "Safe unmount completed",
	})
}

// handleListRcloneRemotes handles GET /api/mounts/rclone/remotes
func (h *Handlers) handleListRcloneRemotes(w http.ResponseWriter, r *http.Request) {
	remotes, err := h.manager.ListRcloneRemotes()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, RcloneRemotesResponse{Remotes: remotes})
}

// handleUpdateMount handles PUT/PATCH /api/mounts/{id}
func (h *Handlers) handleUpdateMount(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	mount, err := h.manager.UpdateMount(id, updates)
	if err != nil {
		if err.Error() == "mount not found" {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, MountResponse{Mount: mount})
}

// handleTriggerMountScan handles POST /api/mounts/{id}/scan
func (h *Handlers) handleTriggerMountScan(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// Get mount info
	mount, err := h.manager.GetMount(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if mount == nil {
		writeError(w, http.StatusNotFound, "mount not found")
		return
	}

	// Check if mount is mounted
	if !mount.Mounted {
		writeJSON(w, http.StatusBadRequest, MountScanResponse{
			Status:    "error",
			Message:   "mount is not currently mounted",
			MountID:   mount.ID,
			MountPath: mount.MountPath,
		})
		return
	}

	// Check if scan function is available
	if h.scanFunc == nil {
		writeJSON(w, http.StatusServiceUnavailable, MountScanResponse{
			Status:    "error",
			Message:   "file watcher not available",
			MountID:   mount.ID,
			MountPath: mount.MountPath,
		})
		return
	}

	// Trigger scan
	fileCount, err := h.scanFunc(mount.MountPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, MountScanResponse{
			Status:    "error",
			Message:   err.Error(),
			MountID:   mount.ID,
			MountPath: mount.MountPath,
		})
		return
	}

	writeJSON(w, http.StatusOK, MountScanResponse{
		Status:    "ok",
		Message:   "scan triggered successfully",
		MountID:   mount.ID,
		MountPath: mount.MountPath,
		FileCount: fileCount,
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

func parseIntOrDefault(s string, defaultVal int) (int, error) {
	var result int
	if s == "" {
		return defaultVal, nil
	}
	err := json.Unmarshal([]byte(s), &result)
	if err != nil {
		return defaultVal, nil
	}
	return result, nil
}
