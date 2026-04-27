package snapshot

import (
	"fmt"

	"github.com/metazla/meta-core/internal/storage"
)

// WipeScope picks which classes of data to clear.
type WipeScope struct {
	Metadata bool // file:* keys + file:__index__
}

// WipeResult reports what was cleared.
type WipeResult struct {
	MetadataDeleted int64 `json:"metadataDeleted"`
}

// Wiper clears metadata from Redis.
type Wiper struct {
	storage *storage.Client
}

func NewWiper(stor *storage.Client) *Wiper {
	return &Wiper{storage: stor}
}

// Wipe applies the requested scope. Returns the count of files removed.
func (w *Wiper) Wipe(scope WipeScope) (*WipeResult, error) {
	if w.storage == nil || !w.storage.IsConnected() {
		return nil, fmt.Errorf("storage not connected")
	}
	res := &WipeResult{}
	if scope.Metadata {
		n, err := w.storage.ClearAllMetadata()
		if err != nil {
			return nil, fmt.Errorf("clear metadata: %w", err)
		}
		res.MetadataDeleted = n
	}
	return res, nil
}
