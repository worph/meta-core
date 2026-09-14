package api

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/metazla/meta-core/internal/identity"
)

// statsSweepTTL is how long a keyspace-walk result stays good. The walk is
// never on the request path (see statsCache), so this only controls how stale
// the library tiles may look, not how slow the endpoint is.
const statsSweepTTL = 60 * time.Second

// fileStatsJSON is the file-backed half of the library: the roots that carry
// filePath+sizeByte+mtimeNano. Gateway / cid-rooted records have no file on
// disk and are counted in Records, not here — the gap between the two numbers
// is exactly "metadata we hold for files we do not have".
type fileStatsJSON struct {
	Count     int   `json:"count"`
	TotalSize int64 `json:"totalSize"`
}

// StatsResponse is the body of GET /api/stats — every headline number the
// dashboard shows, in one request.
//
// Deliberately split by cost. Records/RedisKeys/RedisMemory/Identities are
// O(1) commands (SCARD, DBSIZE, INFO) plus a directory listing, recomputed on
// every call. Files and UDLUsers need a keyspace walk — ~0.5s for the file
// tuples and ~3s for the UDL SCAN on an 88k-record box — so they are served
// from a background-refreshed cache and are null until the first sweep lands.
type StatsResponse struct {
	Records     int64          `json:"records"`
	RedisKeys   int64          `json:"redisKeys"`
	RedisMemory string         `json:"redisMemory"`
	Identities  int            `json:"identities"`
	Files       *fileStatsJSON `json:"files"`
	UDLUsers    *int           `json:"udlUsers"`
	// SweptAt is when the cached half was computed (unix ms), 0 if never.
	SweptAt int64 `json:"sweptAt"`
	// Sweeping reports that a refresh is in flight, so the UI can show the
	// cached numbers as provisional instead of pretending they are live.
	Sweeping bool `json:"sweeping"`
}

// statsCache holds the expensive half of /api/stats.
//
// Why a cache and not just "compute it": the dashboard polls. A 3s Redis walk
// per tick, per open tab, is a self-inflicted outage on a box that is also
// serving playback — the same shape as meta-sort's /api/stats full sweep. So
// the handler NEVER waits on a walk: it returns whatever the last sweep
// produced and kicks off a new one if the old one is stale. `running` makes
// that single-flight, so ten tabs cost one walk.
type statsCache struct {
	mu       sync.Mutex
	files    *fileStatsJSON
	udlUsers *int
	sweptAt  time.Time
	running  bool
}

// snapshot returns the cached values and whether a refresh is in flight.
func (c *statsCache) snapshot() (*fileStatsJSON, *int, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.files, c.udlUsers, c.sweptAt, c.running
}

// refreshIfStale starts a background sweep unless one is already running or
// the last result is still fresh. Returns true if it started one.
func (c *statsCache) refreshIfStale(sweep func() (*fileStatsJSON, *int)) bool {
	c.mu.Lock()
	if c.running || time.Since(c.sweptAt) < statsSweepTTL {
		c.mu.Unlock()
		return false
	}
	c.running = true
	c.mu.Unlock()

	go func() {
		files, udl := sweep()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.running = false
		// A failed sweep leaves the previous numbers in place rather than
		// blanking the tiles; sweptAt still advances so a hard-failing walk
		// (Redis down) does not spin once per poll.
		c.sweptAt = time.Now()
		if files != nil {
			c.files = files
		}
		if udl != nil {
			c.udlUsers = udl
		}
	}()
	return true
}

// handleStats handles GET /api/stats.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	resp := StatsResponse{}

	if n, err := s.storage.CountRoots(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else {
		resp.Records = n
	}
	if n, err := s.storage.CountKeys(); err == nil {
		resp.RedisKeys = n
	}
	if mem, err := s.storage.GetMemoryInfo(); err == nil {
		resp.RedisMemory = mem
	} else {
		resp.RedisMemory = "N/A"
	}
	if list, err := identity.List(s.config.IdentityAccountsDir()); err == nil {
		resp.Identities = len(list)
	}

	s.stats.refreshIfStale(s.sweepStats)
	files, udl, sweptAt, sweeping := s.stats.snapshot()
	resp.Files = files
	resp.UDLUsers = udl
	resp.Sweeping = sweeping
	if !sweptAt.IsZero() {
		resp.SweptAt = sweptAt.UnixMilli()
	}

	writeJSON(w, http.StatusOK, resp)
}

// sweepStats runs the two keyspace walks behind the cached tiles. Called off
// the request path only. A failing half returns nil so the other half still
// lands.
func (s *Server) sweepStats() (*fileStatsJSON, *int) {
	var files *fileStatsJSON
	if tuples, err := s.storage.GetTuplesForAllFiles(); err != nil {
		log.Printf("[API] stats: file tuple sweep failed: %v", err)
	} else {
		fs := fileStatsJSON{Count: len(tuples)}
		for _, t := range tuples {
			fs.TotalSize += t.Size
		}
		files = &fs
	}

	var udl *int
	if stats, err := s.storage.UDLAllUserStats(); err != nil {
		log.Printf("[API] stats: UDL user sweep failed: %v", err)
	} else {
		n := len(stats)
		udl = &n
	}

	return files, udl
}
