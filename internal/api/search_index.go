package api

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metazla/meta-core/internal/storage"
)

// searchIndexRefreshInterval is how often the in-memory search index is rebuilt
// from Redis in the background. Records ingest continuously, so a newly stored
// record becomes searchable within one interval; the rebuild is a single
// batched bulk read (storage.GetSearchIndexTuples), off the request path.
const searchIndexRefreshInterval = 60 * time.Second

// searchIndex is an in-memory snapshot of every record's search haystack
// (hashID + lowercased searchable fields), rebuilt periodically from Redis. It
// exists so POST /api/metadata/search matches in memory (string ops over a
// slice) instead of doing a SCAN+MGET Redis round-trip per record — the latter
// took 40s+ over ~72k records and made a live parallel search path unusable.
type searchIndex struct {
	mu     sync.RWMutex
	tuples []storage.SearchTuple
	ready  atomic.Bool
}

// snapshot returns the current tuples slice and whether the index has built at
// least once. The refresh swaps in a fresh slice under the write lock (never
// mutates in place), so the returned slice is safe to read lock-free.
func (idx *searchIndex) snapshot() ([]storage.SearchTuple, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tuples, idx.ready.Load()
}

// refresh rebuilds the index from Redis once (a batched bulk read).
func (idx *searchIndex) refresh(stor *storage.Client) {
	if stor == nil || !stor.IsConnected() {
		return
	}
	tuples, err := stor.GetSearchIndexTuples()
	if err != nil {
		log.Printf("[API] search index refresh failed: %v", err)
		return
	}
	idx.mu.Lock()
	idx.tuples = tuples
	idx.mu.Unlock()
	idx.ready.Store(true)
}

// runRefreshLoop builds the index immediately, then rebuilds on a ticker for the
// process lifetime.
func (idx *searchIndex) runRefreshLoop(stor *storage.Client) {
	idx.refresh(stor)
	ticker := time.NewTicker(searchIndexRefreshInterval)
	defer ticker.Stop()
	for range ticker.C {
		idx.refresh(stor)
	}
}

// serveIndexedSearch answers a free-text (non-property) query from the in-memory
// index: token-AND match against each record's haystack (or a whole-query hashID
// substring, preserving the legacy behaviour), then fetch full metadata for only
// the <=limit matches. No Redis round-trip per scanned record.
func (s *Server) serveIndexedSearch(w http.ResponseWriter, req MetadataSearchRequest) {
	tuples, ready := s.search.snapshot()
	if !ready {
		// First call before the background build finished: warm inline with the
		// efficient batched read (seconds), never the per-record SCAN fallback.
		s.search.refresh(s.storage)
		tuples, _ = s.search.snapshot()
	}
	tokens := strings.Fields(strings.ToLower(req.Query))
	queryLower := strings.ToLower(req.Query)
	results := make([]MetadataSearchResult, 0, req.Limit)
	for _, t := range tuples {
		if len(results) >= req.Limit {
			break
		}
		if !matchAllTokens(t.Haystack, tokens) &&
			!strings.Contains(strings.ToLower(t.HashID), queryLower) {
			continue
		}
		// Full metadata is cached in the index — return it straight from memory,
		// no Redis round-trip per result.
		results = append(results, MetadataSearchResult{HashID: t.HashID, Metadata: t.Fields})
	}
	writeJSON(w, http.StatusOK, MetadataSearchResponse{
		Results: results,
		Count:   len(results),
		Total:   len(tuples),
	})
}

// matchAllTokens reports whether haystack contains every token (AND semantics).
// An empty token list matches everything.
func matchAllTokens(haystack string, tokens []string) bool {
	for _, t := range tokens {
		if !strings.Contains(haystack, t) {
			return false
		}
	}
	return true
}
