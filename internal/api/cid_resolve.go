package api

import (
	"net/http"

	"github.com/gorilla/mux"
)

// handleGetMetaByCID handles GET /api/meta/{cid}.
//
// Resolves any known CID — midhash256, sha256, IPFS, btih, or anything else
// in the reverse index — to the full metadata document for that file.
// Returns:
//   - 200 with the document on a hit
//   - 404 if the CID isn't registered
//   - 503 if storage is down
//
// CIDs in the URL must be in prefixed-token form ("midhash256:bafk…",
// "sha256:bafk…", "ipfs:bafy…"). Bare CIDs without an algorithm prefix
// won't resolve — callers should pass the token they got from a previous
// metadata document, /api/file/{cid}/info, or a meta-share federated query.
//
// This endpoint is the public read API for the CID-resolution layer and
// is auth-bypassed alongside /api/file/{cid} so that meta-share peers and
// IPFS-style clients can use it without going through Authelia.
func (s *Server) handleGetMetaByCID(w http.ResponseWriter, r *http.Request) {
	cid := mux.Vars(r)["cid"]
	if cid == "" {
		writeError(w, http.StatusBadRequest, "cid is required")
		return
	}
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	uuid, err := s.storage.GetByCID(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if uuid == "" {
		writeErrorSlug(w, http.StatusNotFound, ErrUnknownCID, "no metadata for this CID", false)
		return
	}

	doc, err := s.storage.GetMetadataDocument(uuid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc == nil {
		// Reverse index pointed at a UUID with no remaining keys — stale
		// alias, probably mid-delete race. Treat as not-found.
		writeErrorSlug(w, http.StatusNotFound, ErrUnknownCID, "no metadata for this CID", false)
		return
	}

	// Response envelope mirrors GetMetadataDocument JSON tags (metadata /
	// cids / duplicates), with cid added so the caller can correlate when
	// they fan out N lookups.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cid":        cid,
		"metadata":   doc.Flat,
		"cids":       doc.CIDs,
		"duplicates": doc.Duplicates,
	})
}
