package api

import (
	"net/http"
)

// handleMigrateDualRoots triggers the one-shot sweep that re-unifies
// stranded midhash-rooted entries into their UUID roots. See
// storage.MigrateDualRoots for the data-flow detail and
// docs/api-mediated-access.md for the bug background.
//
// Returns {fixed: N} on success. Safe to call repeatedly — entries already
// migrated are skipped on the next pass.
func (s *Server) handleMigrateDualRoots(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	fixed, err := s.storage.MigrateDualRootsWithTimeout()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"fixed":   fixed,
	})
}
