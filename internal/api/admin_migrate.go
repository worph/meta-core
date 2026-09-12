package api

import (
	"net/http"
	"strconv"
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

// handleSweepLiteratureEchoes removes query echoes stored on literature cards:
// `anilistid` on a site card, and `workcid` / position keys on any card
// (meta-read docs/reading-model.md §7). See storage.SweepLiteratureEchoes for
// why chapters and anchor cards' ids are deliberately left alone.
//
// Dry-run unless `?apply=true`: unlike the migrations above this one deletes
// keys, so a bare call only reports what it would delete. An unparseable
// `apply` is a 400 rather than a silent dry run, so a typo cannot pass for a
// sweep that ran.
//
// Returns {success, dryRun, planned, fixed, byKey} — planned/fixed count
// records, byKey counts fields per key. Safe to call repeatedly: a swept
// record plans nothing on the next pass.
func (s *Server) handleSweepLiteratureEchoes(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	apply := false
	if v := r.URL.Query().Get("apply"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "apply must be true or false")
			return
		}
		apply = parsed
	}

	res, err := s.storage.SweepLiteratureEchoesWithTimeout(apply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"dryRun":  !apply,
		"planned": res.Planned,
		"fixed":   res.Fixed,
		"byKey":   res.ByKey,
	})
}
