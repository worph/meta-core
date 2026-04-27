package mounts

import (
	"context"
	"log"
	"sync"
	"time"
)

// StatsInterval drives the IO-stats sampling cadence. 5 s is the usual sweet
// spot — short enough that throughput numbers feel live, long enough that
// /proc reads aren't a load source themselves.
const StatsInterval = 5 * time.Second

// StatsPoller samples cumulative counters from the local rclone RC API and
// exposes computed rates (bytes/sec, transfers/sec) via GetStats.
//
// Post-consolidation note: every mount type executes through rclone, so
// /proc/fs/cifs/Stats and /proc/self/mountstats are no longer relevant —
// rclone is the only counter source. core/stats is daemon-global across all
// active mounts; the same sample is applied to each mount's status. If
// per-mount byte counters become important, the path forward is rclone stats
// groups (out of scope for this consolidation).
type StatsPoller struct {
	manager *Manager

	mu           sync.RWMutex
	lastSamples  map[string]*StatsSample // mountID -> previous raw sample
	latestStats  map[string]*MountIOStats
	stopChan     chan struct{}
	running      bool
	pollInterval time.Duration
}

// NewStatsPoller constructs a StatsPoller backed by the given manager.
func NewStatsPoller(manager *Manager) *StatsPoller {
	return &StatsPoller{
		manager:      manager,
		lastSamples:  make(map[string]*StatsSample),
		latestStats:  make(map[string]*MountIOStats),
		pollInterval: StatsInterval,
	}
}

// Start launches the sampling loop. Idempotent.
func (p *StatsPoller) Start() {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.stopChan = make(chan struct{})
	stop := p.stopChan
	p.mu.Unlock()

	log.Printf("[StatsPoller] Starting IO stats sampler (interval: %s)", p.pollInterval)
	go p.loop(stop)
}

// Stop terminates the sampling loop. Idempotent.
func (p *StatsPoller) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return
	}
	p.running = false
	if p.stopChan != nil {
		close(p.stopChan)
		p.stopChan = nil
	}
}

// GetStats returns the latest computed IO stats for a mount, or nil if no
// stats have been observed yet.
func (p *StatsPoller) GetStats(mountID string) *MountIOStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.latestStats[mountID]
	if !ok {
		return nil
	}
	// Return a copy so the caller can't mutate our cache.
	cp := *s
	return &cp
}

func (p *StatsPoller) loop(stop <-chan struct{}) {
	// Take the first sample immediately so rates start populating on the
	// second tick instead of waiting two intervals.
	p.tick()

	t := time.NewTicker(p.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.tick()
		}
	}
}

func (p *StatsPoller) tick() {
	mounts, err := p.manager.listMountsWithoutPollingStatus()
	if err != nil {
		log.Printf("[StatsPoller] listMounts failed: %v", err)
		return
	}

	// Sample rclone once per tick — its RC API gives global stats across all
	// active rclone mounts on this host. Both supported mount types (smb,
	// rclone) execute through the same rclone daemon, so the same sample is
	// applied to every mounted entry.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sample, err := SampleRclone(ctx)
	if err != nil {
		log.Printf("[StatsPoller] rclone sample failed: %v", err)
		return
	}
	if sample == nil {
		return
	}

	for _, m := range mounts {
		if !m.Mounted {
			continue
		}

		p.mu.Lock()
		prev := p.lastSamples[m.ID]
		p.lastSamples[m.ID] = sample
		stats := computeStats(prev, sample)
		p.latestStats[m.ID] = stats
		p.mu.Unlock()
	}
}

// computeStats turns a (previous, current) raw-sample pair into the rate-
// based MountIOStats the API exposes. With no previous sample the rates are
// zero — operators will see cumulative counters but Bps/IOPS only on the
// second poll.
func computeStats(prev, cur *StatsSample) *MountIOStats {
	out := &MountIOStats{
		Source:       cur.Source,
		BytesRead:    cur.BytesRead,
		BytesWritten: cur.BytesWritten,
		ReadOps:      cur.ReadOps,
		WriteOps:     cur.WriteOps,
		LastSampleAt: cur.TakenAt.UnixMilli(),
	}
	if prev == nil {
		return out
	}
	dt := cur.TakenAt.Sub(prev.TakenAt).Seconds()
	if dt <= 0 {
		return out
	}
	out.IntervalMs = int64(dt * 1000)

	// Counters can reset to zero (e.g. CIFS module reload, remount). Treat
	// negative deltas as "no movement" rather than letting the rate go
	// negative or implausibly huge.
	if d := cur.BytesRead - prev.BytesRead; d > 0 {
		out.ReadBps = float64(d) / dt
	}
	if d := cur.BytesWritten - prev.BytesWritten; d > 0 {
		out.WriteBps = float64(d) / dt
	}
	if d := cur.ReadOps - prev.ReadOps; d > 0 {
		out.ReadIops = float64(d) / dt
	}
	if d := cur.WriteOps - prev.WriteOps; d > 0 {
		out.WriteIops = float64(d) / dt
	}
	if cur.Source == "rclone" {
		if d := cur.Transfers - prev.Transfers; d > 0 {
			out.TransfersPerSec = float64(d) / dt
		}
	}
	return out
}
