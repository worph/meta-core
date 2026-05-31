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
// CIDs in the URL are bare multibase CIDv1 strings ("bafk…") — the midhash,
// a sibling sha2-256, an IPFS CID, a btih, anything registered in the
// reverse index. A CID is self-describing (its algorithm is the multicodec),
// so there is no <algo>: prefix.
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
	// cids / canonical_cid / duplicates), with cid added so the caller can
	// correlate when they fan out N lookups. canonical_cid is derived by
	// rank from the cids key-set on read — it is not a stored field.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cid":          cid,
		"metadata":     doc.Flat,
		"cids":         doc.CIDs,
		"canonical_cid": doc.Canonical,
		"duplicates":   doc.Duplicates,
	})
}
