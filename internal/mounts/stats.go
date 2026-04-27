package mounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// StatsSample is a raw cumulative-counter snapshot taken at one instant in
// time. The poller keeps the previous sample per mount and computes rates
// (Bps, transfers/sec) from the delta to the latest sample.
//
// Post-consolidation: rclone is the only sample source, since all mount types
// execute through the local rclone daemon.
type StatsSample struct {
	TakenAt      time.Time
	Source       string // "rclone"
	BytesRead    int64
	BytesWritten int64
	ReadOps      int64
	WriteOps     int64
	// File-level transfer count from rclone's RC API. Distinct from ReadOps
	// (which is unused for rclone — kernel ops counters are not exposed
	// through FUSE).
	Transfers int64
}

// rcloneCoreStats matches the RC API response from POST /core/stats.
type rcloneCoreStats struct {
	Bytes          int64 `json:"bytes"`
	TotalBytes     int64 `json:"totalBytes"`
	Transfers      int64 `json:"transfers"`
	TotalTransfers int64 `json:"totalTransfers"`
}

// SampleRclone queries the local rclone RC API for the cumulative bytes +
// transfers counters. The numbers are global across all active mounts on this
// host — rclone tracks stats per daemon, not per mount. The poller reuses one
// sample for every mounted entry on a tick.
func SampleRclone(ctx context.Context) (*StatsSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:5572/core/stats", nil)
	if err != nil {
		return nil, fmt.Errorf("build rclone stats request: %w", err)
	}
	req.SetBasicAuth("admin", "admin")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call rclone /core/stats: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rclone /core/stats returned %d", resp.StatusCode)
	}

	var stats rcloneCoreStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode rclone stats: %w", err)
	}

	return &StatsSample{
		Source:    "rclone",
		TakenAt:   time.Now(),
		BytesRead: stats.Bytes,
		Transfers: stats.Transfers,
	}, nil
}
