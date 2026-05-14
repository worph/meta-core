package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// handleSchemaGet returns the live per-field schema derived from meta:events.
func (s *Server) handleSchemaGet(w http.ResponseWriter, r *http.Request) {
	if s.schemaIndexer == nil {
		http.Error(w, "schema indexer not running", http.StatusServiceUnavailable)
		return
	}
	snap := s.schemaIndexer.Snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// handleSchemaRescan re-emits a set event for every existing property via the
// meta publisher, waits for the indexer to drain, and returns the refreshed
// schema. Equivalent to a full SCAN-based rebuild without locking Redis.
func (s *Server) handleSchemaRescan(w http.ResponseWriter, r *http.Request) {
	if s.schemaIndexer == nil {
		http.Error(w, "schema indexer not running", http.StatusServiceUnavailable)
		return
	}

	count, err := s.RepublishMetadata()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.schemaIndexer.WaitForDrain(10 * time.Second)

	snap := s.schemaIndexer.Snapshot()
	snap.Source = "rescan"

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"published_events": count,
		"schema":           snap,
	})
}
