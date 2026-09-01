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

// handleMigrateDomainScreen triggers the one-shot sweep that rewrites the
// retired `domain=film|tv` values to `screen` and backfills `workForm` from
// `contentKind` (METADATA_KEYS.md §14.17). See
// storage.MigrateDomainScreen for why the vocabulary change is paid in the
// data rather than with a read-side alias.
//
// Returns {fixed: N} — records changed, not records examined. Safe to call
// repeatedly: a record already on the current vocabulary is skipped.
func (s *Server) handleMigrateDomainScreen(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	fixed, err := s.storage.MigrateDomainScreenWithTimeout()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"fixed":   fixed,
	})
}
