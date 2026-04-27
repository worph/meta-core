package snapshot

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/metazla/meta-core/internal/storage"
)

// ExportOptions controls what an export includes.
type ExportOptions struct {
	IncludePluginOutput bool
	// PluginOutputDir is the absolute path on disk that holds plugin-produced
	// artifacts (typically $FILES_PATH/plugin). Required when
	// IncludePluginOutput is true.
	PluginOutputDir string
	// SourceHost is recorded in the manifest so imports can attribute
	// where the snapshot came from (defaults to OS hostname).
	SourceHost string
}

// Exporter writes snapshot ZIPs from a live Redis store.
type Exporter struct {
	storage *storage.Client
}

func NewExporter(stor *storage.Client) *Exporter {
	return &Exporter{storage: stor}
}

// Export streams a ZIP archive to w. The archive layout is:
//
//	manifest.json
//	index.json
//	metadata/{cid}.json    (one per known CID)
//
// Optional sections (hash-index, plugin-output) are gated by ExportOptions
// but are not yet implemented — they live on filesystems meta-core does not
// own, and need cross-service collection. The flag is plumbed for forward
// compatibility but does nothing today.
func (e *Exporter) Export(w io.Writer, opts ExportOptions) (Manifest, error) {
	if e.storage == nil || !e.storage.IsConnected() {
		return Manifest{}, fmt.Errorf("storage not connected")
	}

	hashIDs, err := e.storage.GetAllHashIDs()
	if err != nil {
		return Manifest{}, fmt.Errorf("list hashIDs: %w", err)
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	// 1. manifest.json — written last after we know FileCount, but we need
	//    the entry order to put it first in the archive for readers. Solve
	//    by buffering in memory: it's tiny.
	host := opts.SourceHost
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		}
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		SourceHost:    host,
		FileCount:     len(hashIDs),
		Includes: Includes{
			Metadata:     true,
			PluginOutput: opts.IncludePluginOutput,
		},
	}
	if err := writeJSON(zw, ManifestFile, manifest); err != nil {
		return Manifest{}, fmt.Errorf("write manifest: %w", err)
	}

	// 2. index.json — flat list of CIDs. Redundant with metadata/ but
	//    cheap and useful for readers that only need the keyset.
	if err := writeJSON(zw, IndexFile, IndexFileBody{HashIDs: hashIDs}); err != nil {
		return Manifest{}, fmt.Errorf("write index: %w", err)
	}

	// 3. metadata/{cid}.json — one nested document per CID.
	for _, hashID := range hashIDs {
		flat, err := e.storage.GetMetadataFlat(hashID)
		if err != nil {
			log.Printf("[Snapshot] export: skipping %s: %v", hashID, err)
			continue
		}
		if len(flat) == 0 {
			continue
		}
		nested := Reconstruct(flat)
		entry := MetadataDir + hashID + ".json"
		if err := writeJSON(zw, entry, nested); err != nil {
			return Manifest{}, fmt.Errorf("write %s: %w", entry, err)
		}
	}

	// 4. cache/plugin-output/* — optional. Walks the on-disk plugin dir and
	//    streams each file into the ZIP. Heavy (~120 MB on a populated dev
	//    stack), so it's gated by the ExportOptions toggle.
	if opts.IncludePluginOutput {
		if opts.PluginOutputDir == "" {
			return Manifest{}, fmt.Errorf("PluginOutputDir is required when IncludePluginOutput is set")
		}
		n, err := addPluginOutput(zw, opts.PluginOutputDir)
		if err != nil {
			return Manifest{}, fmt.Errorf("add plugin-output: %w", err)
		}
		log.Printf("[Snapshot] exported %d plugin-output files from %s", n, opts.PluginOutputDir)
	}

	log.Printf("[Snapshot] exported %d files (host=%s, pluginOutput=%v)",
		len(hashIDs), host, opts.IncludePluginOutput)
	return manifest, nil
}

func writeJSON(zw *zip.Writer, name string, body interface{}) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(body)
}
