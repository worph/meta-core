package mounts

import "time"

// MountType represents the type of mount.
//
// All types execute through the local rclone daemon. "smb" is a UX-level alias
// for rclone's :smb: backend — operators fill in SMB-specific fields and the
// mount-watcher synthesises the rclone remote spec at mount time. "rclone" is
// the generic escape hatch for any pre-defined remote in rclone.conf.
//
// Native NFS/CIFS handlers were removed in favour of the rclone-only path —
// kernel inotify wasn't reliable on those mounts anyway and the watcher polls
// regardless.
type MountType string

const (
	MountTypeSMB    MountType = "smb"
	MountTypeRclone MountType = "rclone"
)

// Polling constants
const (
	DefaultPollingIntervalMs = 30000 // 30 seconds
	MinPollingIntervalMs     = 5000  // 5 seconds
)

// VFS cache defaults — applied per-mount when the caller leaves the field
// blank. Values are strings so they round-trip rclone's parsers (SizeSuffix,
// Duration) untouched.
const (
	DefaultCacheMaxSize  = "50G"
	DefaultCacheMaxAge   = "24h"
	DefaultDirCacheTime  = "5m"
)

// MountConfig represents a mount configuration
type MountConfig struct {
	// Common fields
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Type           MountType `json:"type"`
	Enabled        bool      `json:"enabled"`
	DesiredMounted bool      `json:"desiredMounted"`
	MountPath      string    `json:"mountPath"`

	// SMB-specific fields (also valid for type=smb routed through rclone-smb)
	SMBServer           string `json:"smbServer,omitempty"`
	SMBShare            string `json:"smbShare,omitempty"`
	SMBUsername         string `json:"smbUsername,omitempty"`
	SMBPasswordObscured string `json:"smbPasswordObscured,omitempty"`
	SMBDomain           string `json:"smbDomain,omitempty"`

	// rclone-specific fields (escape hatch — references a pre-defined remote)
	RcloneRemote string `json:"rcloneRemote,omitempty"`
	RclonePath   string `json:"rclonePath,omitempty"`

	// VFS cache configuration — applied to the rclone mount call. All three
	// fields are optional; blanks fall back to Default* constants above.
	// CacheMaxSize uses rclone's SizeSuffix ("50G", "10M"), CacheMaxAge and
	// DirCacheTime use rclone's Duration ("24h", "30m").
	CacheMaxSize string `json:"cacheMaxSize,omitempty"`
	CacheMaxAge  string `json:"cacheMaxAge,omitempty"`
	DirCacheTime string `json:"dirCacheTime,omitempty"`

	// Polling configuration
	PollingEnabled    bool `json:"pollingEnabled"`
	PollingIntervalMs int  `json:"pollingIntervalMs,omitempty"` // Default: 30000
}

// MountStatus represents the runtime status of a mount
type MountStatus struct {
	MountConfig
	Mounted        bool   `json:"mounted"`
	Error          string `json:"error,omitempty"`
	LastChecked    int64  `json:"lastChecked"`
	PollingActive  bool   `json:"pollingActive,omitempty"`
	LastPolledScan int64  `json:"lastPolledScan,omitempty"`
	// Progress tracking — surface the poller's adaptive scan state to the UI.
	Scanning             bool  `json:"scanning,omitempty"`
	CurrentScanStartedAt int64 `json:"currentScanStartedAt,omitempty"` // ms; non-zero only while Scanning
	LastScanDurationMs   int64 `json:"lastScanDurationMs,omitempty"`   // duration of the most recently completed scan
	NextScanAt           int64 `json:"nextScanAt,omitempty"`           // ms; planned start of the next adaptive scan
	// Live IO stats — sampled by StatsPoller from /proc/fs/cifs/Stats,
	// /proc/self/mountstats, or rclone's RC API depending on mount type.
	IOStats *MountIOStats `json:"ioStats,omitempty"`
}

// MountIOStats captures throughput + operation rate for a single mount.
// Cumulative counters (BytesRead, ReadOps) come straight from the source;
// rates (ReadBps, ReadIops) are computed by StatsPoller as the delta between
// the last two samples divided by the interval.
type MountIOStats struct {
	Source       string  `json:"source"` // "rclone" — only source after the rclone-only consolidation
	BytesRead    int64   `json:"bytesRead"`
	BytesWritten int64   `json:"bytesWritten,omitempty"`
	ReadBps      float64 `json:"readBps"`
	WriteBps     float64 `json:"writeBps,omitempty"`
	ReadOps      int64   `json:"readOps,omitempty"`
	WriteOps     int64   `json:"writeOps,omitempty"`
	// ReadIops and WriteIops are op counters per second — number of read/write
	// requests issued by the kernel against the share. Distinct from
	// TransfersPerSec which is a file-level metric only relevant to rclone.
	ReadIops        float64 `json:"readIops,omitempty"`
	WriteIops       float64 `json:"writeIops,omitempty"`
	TransfersPerSec float64 `json:"transfersPerSec,omitempty"` // rclone-only
	LastSampleAt    int64   `json:"lastSampleAt"`              // ms
	IntervalMs      int64   `json:"intervalMs,omitempty"`      // gap between the two samples used to compute rates
}

// MountsFile represents the mounts.json file structure
type MountsFile struct {
	Version int           `json:"version"`
	Mounts  []MountConfig `json:"mounts"`
}

// RcloneRemote represents an rclone remote
type RcloneRemote struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// CreateMountRequest is the request body for creating a mount
type CreateMountRequest struct {
	Name    string    `json:"name"`
	Type    MountType `json:"type"`
	Enabled *bool     `json:"enabled,omitempty"` // Pointer to detect if set

	// SMB
	SMBServer   string `json:"smbServer,omitempty"`
	SMBShare    string `json:"smbShare,omitempty"`
	SMBUsername string `json:"smbUsername,omitempty"`
	SMBPassword string `json:"smbPassword,omitempty"` // Plain text, will be obscured
	SMBDomain   string `json:"smbDomain,omitempty"`

	// rclone
	RcloneRemote string `json:"rcloneRemote,omitempty"`
	RclonePath   string `json:"rclonePath,omitempty"`

	// VFS cache (optional — falls back to Default* constants when unset)
	CacheMaxSize string `json:"cacheMaxSize,omitempty"`
	CacheMaxAge  string `json:"cacheMaxAge,omitempty"`
	DirCacheTime string `json:"dirCacheTime,omitempty"`

	// Polling
	PollingEnabled    *bool `json:"pollingEnabled,omitempty"`
	PollingIntervalMs *int  `json:"pollingIntervalMs,omitempty"`
}

// MountScanResponse is the response for mount scan operations
type MountScanResponse struct {
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
	MountID   string `json:"mountId"`
	MountPath string `json:"mountPath"`
	FileCount int    `json:"fileCount,omitempty"`
}

// MountResponse is the response for mount operations
type MountResponse struct {
	Mount *MountStatus `json:"mount,omitempty"`
}

// MountsListResponse is the response for listing mounts
type MountsListResponse struct {
	Mounts []MountStatus `json:"mounts"`
}

// RcloneRemotesResponse is the response for listing rclone remotes
type RcloneRemotesResponse struct {
	Remotes []RcloneRemote `json:"remotes"`
}

// StatusResponse is a generic status response
type StatusResponse struct {
	Status     string      `json:"status"`
	Message    string      `json:"message,omitempty"`
	GateStatus interface{} `json:"gateStatus,omitempty"`
}

// Timestamp helpers
func NowMS() int64 {
	return time.Now().UnixMilli()
}
