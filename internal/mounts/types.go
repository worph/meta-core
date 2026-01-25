package mounts

import "time"

// MountType represents the type of mount
type MountType string

const (
	MountTypeNFS    MountType = "nfs"
	MountTypeSMB    MountType = "smb"
	MountTypeRclone MountType = "rclone"
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
	Options        string    `json:"options,omitempty"`

	// NFS-specific fields
	NFSServer string `json:"nfsServer,omitempty"`
	NFSPath   string `json:"nfsPath,omitempty"`

	// SMB-specific fields
	SMBServer           string `json:"smbServer,omitempty"`
	SMBShare            string `json:"smbShare,omitempty"`
	SMBUsername         string `json:"smbUsername,omitempty"`
	SMBPasswordObscured string `json:"smbPasswordObscured,omitempty"`
	SMBDomain           string `json:"smbDomain,omitempty"`

	// rclone-specific fields
	RcloneRemote string `json:"rcloneRemote,omitempty"`
	RclonePath   string `json:"rclonePath,omitempty"`
}

// MountStatus represents the runtime status of a mount
type MountStatus struct {
	MountConfig
	Mounted     bool   `json:"mounted"`
	Error       string `json:"error,omitempty"`
	LastChecked int64  `json:"lastChecked"`
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
	Name         string    `json:"name"`
	Type         MountType `json:"type"`
	Enabled      *bool     `json:"enabled,omitempty"` // Pointer to detect if set
	Options      string    `json:"options,omitempty"`

	// NFS
	NFSServer string `json:"nfsServer,omitempty"`
	NFSPath   string `json:"nfsPath,omitempty"`

	// SMB
	SMBServer   string `json:"smbServer,omitempty"`
	SMBShare    string `json:"smbShare,omitempty"`
	SMBUsername string `json:"smbUsername,omitempty"`
	SMBPassword string `json:"smbPassword,omitempty"` // Plain text, will be obscured
	SMBDomain   string `json:"smbDomain,omitempty"`

	// rclone
	RcloneRemote string `json:"rcloneRemote,omitempty"`
	RclonePath   string `json:"rclonePath,omitempty"`
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
