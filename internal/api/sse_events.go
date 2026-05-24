package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// SSE event stream endpoints for the file:events and meta:events Redis Streams.
//
// Wire contract (see docs/api-mediated-access.md):
//   - One SSE event per Redis Stream entry.
//   - `id:` is the opaque Redis Stream entry ID (`<ms>-<seq>`); consumers
//     MUST treat it as a black-box string they echo back via Last-Event-ID.
//   - `event:` is the `type` field from the underlying entry.
//   - `data:` is the JSON-encoded rest of the entry (all fields except `type`).
//   - Heartbeats are SSE comment lines (`:keep-alive\n\n`) every 30s of silence.
//   - On reconnect with Last-Event-ID, resume from the next entry. If the
//     cursor has been trimmed by retention, emit one `event: gap` payload
//     before resuming from the oldest available entry.

const (
	sseHeartbeatInterval = 30 * time.Second

	// sseReadBlock controls how long each XREAD blocks before returning.
	// Much shorter than sseHeartbeatInterval so the handler stays responsive
	// to client disconnects and the heartbeat check runs frequently enough
	// that the keep-alive fires within a few seconds of its nominal cadence.
	sseReadBlock = 2 * time.Second

	// sseReadCount caps how many entries one XREAD returns. Small enough
	// to keep per-flush memory low, large enough to drain bursts cheaply.
	sseReadCount = 100
)

// handleEventsFiles streams the file:events Redis Stream as SSE.
func (s *Server) handleEventsFiles(w http.ResponseWriter, r *http.Request) {
	s.streamSSE(w, r, "file:events")
}

// handleEventsMeta streams the meta:events Redis Stream as SSE.
func (s *Server) handleEventsMeta(w http.ResponseWriter, r *http.Request) {
	s.streamSSE(w, r, "meta:events")
}

// streamSSE is the shared SSE pump shared by both endpoints. One handler per
// stream — there is no fan-out; each subscriber holds its own XREAD loop.
// That's fine at our scale (≤20 subscribers; one per consumer service).
func (s *Server) streamSSE(w http.ResponseWriter, r *http.Request, stream string) {
	if !s.storage.IsConnected() {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// The server-wide WriteTimeout (30s on Server.server in server.go) is
	// fine for ordinary handlers but lethal for SSE — it would close every
	// connection at exactly 30s, racing the heartbeat. Disable it
	// per-request via http.NewResponseController. SSE endpoints never need
	// a write deadline; the client disconnect path already cleans up.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// Older transports (or nested response writers) may not support
		// this. Log and continue — the 30s cap will still apply, but the
		// handler is still useful for short-lived subscriptions.
		log.Printf("[SSE %s] could not disable write deadline: %v", stream, err)
	}

	// SSE response headers. X-Accel-Buffering disables nginx response
	// buffering; the hash-lock sidecar uses nginx, so this matters.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	client := s.storage.GetRedisClient()
	if client == nil {
		// Storage was just disconnected — close cleanly.
		return
	}

	ctx := r.Context()
	cursor := resolveStartCursor(ctx, client, stream, r.Header.Get("Last-Event-ID"), w, flusher)

	lastWrite := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return
		}

		// Bounded BLOCK lets us re-check ctx and emit heartbeats. The
		// context's deadline isn't propagated into XREAD's BLOCK arg —
		// that's a poll-style timeout on Redis itself — so we keep BLOCK
		// short.
		readCtx, cancel := context.WithTimeout(ctx, sseReadBlock+2*time.Second)
		result, err := client.XRead(readCtx, &redis.XReadArgs{
			Streams: []string{stream, cursor},
			Block:   sseReadBlock,
			Count:   sseReadCount,
		}).Result()
		cancel()

		if err != nil {
			// redis.Nil = BLOCK timeout with no entries. Anything else
			// while the client is still connected is a real failure.
			if errors.Is(err, redis.Nil) || isContextTimeout(err) {
				// fall through to heartbeat check
			} else if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			} else {
				log.Printf("[SSE %s] XREAD error: %v", stream, err)
				return
			}
		}

		for _, str := range result {
			for _, msg := range str.Messages {
				writeSSEEntry(w, flusher, msg)
				cursor = msg.ID
				lastWrite = time.Now()
			}
		}

		// Heartbeat if quiet for >= the interval. Comment line; SSE
		// clients ignore it per spec.
		if time.Since(lastWrite) >= sseHeartbeatInterval {
			if _, err := fmt.Fprint(w, ":keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			lastWrite = time.Now()
		}
	}
}

// resolveStartCursor decides where to start reading.
//   - No Last-Event-ID → start at "$" (only new entries).
//   - Last-Event-ID provided AND still in stream → resume from it.
//   - Last-Event-ID provided BUT trimmed → emit one synthetic `gap` event
//     and resume from the oldest available entry's predecessor "0", so the
//     next XREAD picks up everything still in the stream.
//
// The "still in stream" check is cheap: one XRANGE for the first entry.
func resolveStartCursor(ctx context.Context, client *redis.Client, stream, lastID string, w http.ResponseWriter, flusher http.Flusher) string {
	if lastID == "" {
		return "$"
	}

	// One-entry XRANGE to find the oldest ID currently in the stream.
	xCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	entries, err := client.XRangeN(xCtx, stream, "-", "+", 1).Result()
	if err != nil {
		// Treat any error as "stream not available right now" and resume
		// from the cursor — XREAD will either find it or block until new
		// entries arrive.
		log.Printf("[SSE %s] XRANGE probe failed: %v (resuming from cursor)", stream, err)
		return lastID
	}

	if len(entries) == 0 {
		// Stream is empty. Cursor may be from a previous incarnation; safest
		// is to resume from the cursor — XREAD against an empty stream
		// returns nothing and blocks until new entries arrive.
		return lastID
	}

	oldest := entries[0].ID
	if compareStreamIDs(lastID, oldest) >= 0 {
		// Cursor is at or past the oldest — fine to resume.
		return lastID
	}

	// Cursor was trimmed. Emit a gap event so consumers that care can react.
	gap := map[string]string{
		"requested":  lastID,
		"resumeFrom": oldest,
		"reason":     "retention",
	}
	payload, _ := json.Marshal(gap)
	fmt.Fprintf(w, "event: gap\ndata: %s\n\n", string(payload))
	flusher.Flush()

	// Return "0" so the next XREAD picks up entries starting from the
	// stream's true minimum — including `oldest` itself.
	return "0"
}

// writeSSEEntry serialises one Redis Stream entry as an SSE event.
//
// `event:` comes from the entry's `type` field (no prefix munging). `data:`
// is a JSON object containing all OTHER fields. Field values from the Redis
// stream are interface{} (typically strings); we encode whatever the stream
// holds.
func writeSSEEntry(w http.ResponseWriter, flusher http.Flusher, msg redis.XMessage) {
	eventType := "message"
	data := make(map[string]interface{}, len(msg.Values))
	for k, v := range msg.Values {
		if k == "type" {
			if s, ok := v.(string); ok {
				eventType = s
				continue
			}
		}
		data[k] = v
	}

	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("[SSE] failed to marshal entry %s: %v", msg.ID, err)
		return
	}

	// Spec: each event ends with a blank line. id/event/data lines are
	// terminated with single \n.
	fmt.Fprintf(w, "id: %s\n", msg.ID)
	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", string(payload))
	flusher.Flush()
}

// compareStreamIDs compares two Redis Stream IDs of the form "<ms>-<seq>".
// Returns -1/0/+1. Missing or malformed IDs sort as zero parts.
func compareStreamIDs(a, b string) int {
	aMs, aSeq := parseStreamID(a)
	bMs, bSeq := parseStreamID(b)
	switch {
	case aMs < bMs:
		return -1
	case aMs > bMs:
		return 1
	case aSeq < bSeq:
		return -1
	case aSeq > bSeq:
		return 1
	default:
		return 0
	}
}

func parseStreamID(id string) (int64, int64) {
	parts := strings.SplitN(id, "-", 2)
	ms, _ := strconv.ParseInt(parts[0], 10, 64)
	var seq int64
	if len(parts) == 2 {
		seq, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	return ms, seq
}

// isContextTimeout returns true for the context deadline error wrapped by
// go-redis when XREAD's read context fires. We treat it the same as
// redis.Nil — i.e. an empty poll cycle, not a fatal error.
func isContextTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// go-redis sometimes wraps this in its own error type. String match is
	// a safety net.
	return strings.Contains(err.Error(), "context deadline exceeded")
}
