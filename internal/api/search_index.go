package api

import (
	"log"
	"net/http"
	"sort"
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

// searchIndexReconcileEvery is how many refresh ticks pass between full
// rebuilds. Every other tick is incremental. 30 ticks × 60 s = a full
// reconcile every 30 minutes — see runRefreshLoop for why one is kept at all.
const searchIndexReconcileEvery = 30

// searchIndex is an in-memory snapshot of every record's search haystack
// (hashID + lowercased searchable fields), kept current from Redis. It exists
// so POST /api/metadata/search matches in memory (string ops over a slice)
// instead of doing a SCAN+MGET Redis round-trip per record — the latter took
// 40s+ over ~72k records and made a live parallel search path unusable.
//
// # Two update paths, and why
//
// `refresh` rebuilds everything: a whole-keyspace SCAN, batched MGETs, and a
// complete fresh copy. That is O(corpus) regardless of how much changed, and it
// used to run every 60 s forever. Measured on a 19 622-record staging corpus
// (2026-08-30): the loop accounted for ~45 % of ALL Redis CPU, and the Go side
// held 783 MB for data Redis stores in 135 MB — largely the double copy.
//
// `refreshIncremental` re-reads only the records that changed, tracked by
// storage's dirty set (search_dirty.go). Cost is O(change). It is the steady
// state; the full rebuild runs once at startup (there is nothing to be
// incremental against) and then only as an explicit repair.
//
// The distinction matters because meta-core is the platform's permanent,
// additive store — every search hit on every gateway is retained — so a rebuild
// priced against the whole corpus is a cost that only ever grows.
type searchIndex struct {
	mu sync.RWMutex
	// byID is the authoritative content; tuples is a stable-order view of it
	// rebuilt on each update. Map iteration order in Go is randomised, so
	// serving a limited query straight off the map would return a different
	// subset each call — the slice is what keeps results stable.
	byID   map[string]storage.SearchTuple
	tuples []storage.SearchTuple
	ready  atomic.Bool
}

// snapshot returns the current tuples slice and whether the index has built at
// least once. Every update swaps in a fresh slice under the write lock (never
// mutates in place), so the returned slice is safe to read lock-free.
func (idx *searchIndex) snapshot() ([]storage.SearchTuple, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.tuples, idx.ready.Load()
}

// rebuildViewLocked regenerates the stable-order slice from byID. Caller holds
// the write lock.
func (idx *searchIndex) rebuildViewLocked() {
	view := make([]storage.SearchTuple, 0, len(idx.byID))
	for _, t := range idx.byID {
		view = append(view, t)
	}
	// Stable across rebuilds so a limited query returns a consistent page
	// rather than a fresh random subset every interval.
	sort.Slice(view, func(i, j int) bool { return view[i].HashID < view[j].HashID })
	idx.tuples = view
}

// refresh rebuilds the whole index from Redis (a batched bulk read). Used for
// the initial build and as an explicit repair — not the steady state.
func (idx *searchIndex) refresh(stor *storage.Client) {
	if stor == nil || !stor.IsConnected() {
		return
	}
	tuples, err := stor.GetSearchIndexTuples()
	if err != nil {
		log.Printf("[API] search index refresh failed: %v", err)
		return
	}
	byID := make(map[string]storage.SearchTuple, len(tuples))
	for _, t := range tuples {
		byID[t.HashID] = t
	}
	idx.mu.Lock()
	idx.byID = byID
	idx.rebuildViewLocked()
	idx.mu.Unlock()
	idx.ready.Store(true)
}

// refreshIncremental drains the dirty set and patches only those records.
// Returns whether more dirty entries remain, so a burst is caught up over
// consecutive passes rather than one per interval.
//
// A record in the dirty set but absent from storage was deleted and is evicted
// here — otherwise it would keep answering searches until the next full
// rebuild. A batch that fails after the (destructive) drain is re-marked by
// storage so it is retried rather than silently lost.
func (idx *searchIndex) refreshIncremental(stor *storage.Client) (more bool) {
	if stor == nil || !stor.IsConnected() {
		return false
	}
	ids, more, err := stor.DrainDirty()
	if err != nil {
		log.Printf("[API] search index drain failed: %v", err)
		return false
	}
	if len(ids) == 0 {
		return false
	}
	tuples, gone, err := stor.GetSearchIndexTuplesFor(ids)
	if err != nil {
		stor.MarkDirty(ids...)
		log.Printf("[API] incremental search index refresh failed: %v", err)
		return false
	}

	idx.mu.Lock()
	if idx.byID == nil {
		idx.byID = make(map[string]storage.SearchTuple, len(tuples))
	}
	for _, t := range tuples {
		idx.byID[t.HashID] = t
	}
	for _, id := range gone {
		delete(idx.byID, id)
	}
	idx.rebuildViewLocked()
	idx.mu.Unlock()

	log.Printf("[API] search index: %d updated, %d evicted (%d records)", len(tuples), len(gone), len(idx.tuples))
	return more
}

// runRefreshLoop builds the index once in full, then keeps it current from the
// dirty set, with a periodic full reconcile as a safety net.
//
// The reconcile exists because a *missed* dirty mark is silent: no error, just
// a record whose indexed copy quietly diverges from storage until something
// forces a rebuild. Marking is spread across eight write paths plus anything
// added later, and the cost of getting it wrong is invisible — so the index
// should not depend on that coverage being perfect forever. Every
// searchIndexReconcileEvery-th tick pays the old O(corpus) pass and repairs
// whatever drifted.
//
// That still drops the full rebuild from every 60 s to every 30 min — a ~30×
// reduction in the steady-state cost that measured at ~45 % of all Redis CPU —
// while keeping the self-healing property that made the naive loop safe.
func (idx *searchIndex) runRefreshLoop(stor *storage.Client) {
	idx.refresh(stor)
	ticker := time.NewTicker(searchIndexRefreshInterval)
	defer ticker.Stop()
	ticks := 0
	for range ticker.C {
		ticks++
		if ticks%searchIndexReconcileEvery == 0 {
			idx.refresh(stor)
			continue
		}
		// Drain until caught up, so a burst larger than one batch does not take
		// one interval per batch to land.
		for idx.refreshIncremental(stor) {
		}
	}
}

// serveIndexedSearch answers a search from the in-memory index: a token-AND
// match against each record's haystack (or a whole-query hashID substring,
// preserving the legacy behaviour) AND every structured filter, then returns
// the full metadata for the <=limit survivors. No Redis round-trip per scanned
// record — matching and results both come from memory.
//
// This is the ONLY search path. Free text, filters, both, or neither all land
// here; only the `hashId` exact-match branch in handleSearchMetadata bypasses it.
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
	filters := normalizedFilters(req)
	results := make([]MetadataSearchResult, 0, req.Limit)
	for _, t := range tuples {
		if len(results) >= req.Limit {
			break
		}
		if req.Query != "" &&
			!matchAllTokens(t.Haystack, tokens) &&
			!strings.Contains(strings.ToLower(t.HashID), queryLower) {
			continue
		}
		if !recordMatchesFilters(t.Fields, filters) {
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

// normalizedFilters returns the request's structured filters with the legacy
// Property/PropertyValue pair folded in as one more entry.
//
// The legacy pair keeps Exact=false so its long-standing substring semantics
// are unchanged for existing callers; new callers pass Filters and choose.
func normalizedFilters(req MetadataSearchRequest) []MetadataFilter {
	if req.Property == "" || req.PropertyValue == "" {
		return req.Filters
	}
	out := make([]MetadataFilter, 0, len(req.Filters)+1)
	out = append(out, req.Filters...)
	return append(out, MetadataFilter{
		Property: req.Property,
		Values:   []string{req.PropertyValue},
	})
}

// recordMatchesFilters reports whether fields satisfies every filter (AND).
func recordMatchesFilters(fields map[string]string, filters []MetadataFilter) bool {
	for i := range filters {
		if !recordMatchesFilter(fields, &filters[i]) {
			return false
		}
	}
	return true
}

// recordMatchesFilter reports whether fields satisfies one filter (OR within
// its values), probing BOTH storage shapes a field can take:
//
//   - a flat scalar/csv-set field — `fileType = "video"`, `videoType = "tv,anime"`;
//   - a key-set member — `genres/Action = "true"`, `source/gateway:nyaa.si = "true"`
//     (METADATA_KEYS "How the store is shaped").
//
// Probing both is what lets one filter shape serve the whole vocabulary. The
// gateway needs a `genres:action` filter to find a record whose genre lives in
// the key `genres/Action`, and a `fileType:video` filter to find one whose value
// lives in the field `fileType` — without meta-core knowing which fields are
// key-sets. It also means this stays a SUPERSET of the gateway's own
// `record_matches`, which is the contract: meta-core pre-filters, the gateway
// re-filters authoritatively.
//
// A filter with no usable value constrains nothing (matches). A filter whose
// field is absent fails closed, matching `record_matches`.
func recordMatchesFilter(fields map[string]string, f *MetadataFilter) bool {
	values := make([]string, 0, len(f.Values))
	for _, v := range f.Values {
		if v = strings.TrimSpace(v); v != "" {
			values = append(values, v)
		}
	}
	if f.Property == "" || len(values) == 0 {
		return true
	}

	// Key-set shape: `<property>/<value>` present and truthy. Try the exact
	// key first — that hits whenever the caller's casing matches what was
	// written, which is the common case and costs one map probe.
	for _, v := range values {
		if isTruthyMember(fields[f.Property+"/"+v]) {
			return true
		}
	}
	// Members are written verbatim (`genres/Action`) while query values arrive
	// lowercased, so fall back to one case-insensitive sweep. Single pass over
	// the record's fields for ALL values, not one pass per value.
	wanted := make(map[string]struct{}, len(values))
	for _, v := range values {
		wanted[strings.ToLower(v)] = struct{}{}
	}
	prefix := strings.ToLower(f.Property) + "/"
	for k, val := range fields {
		if len(k) <= len(prefix) || !strings.EqualFold(k[:len(prefix)], prefix) {
			continue
		}
		if _, ok := wanted[strings.ToLower(k[len(prefix):])]; ok && isTruthyMember(val) {
			return true
		}
	}

	// Flat shape: the field's own value, split on `,` for csv-set fields.
	fieldValue, ok := fields[f.Property]
	if !ok {
		return false
	}
	for _, piece := range strings.Split(fieldValue, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		for _, v := range values {
			if f.Exact {
				if strings.EqualFold(piece, v) {
					return true
				}
			} else if strings.Contains(strings.ToLower(piece), strings.ToLower(v)) {
				return true
			}
		}
	}
	return false
}

// isTruthyMember reports whether a key-set member's value asserts membership.
// Mirrors the gateway's `is_truthy_member`: presence with a non-empty,
// non-"false"/"0" value.
func isTruthyMember(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v != "" && v != "false" && v != "0"
}
