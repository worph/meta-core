package snapshot

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/metazla/meta-core/internal/storage"
)

// Mode controls how an import reconciles existing CIDs.
type Mode string

const (
	// ModeReplace deletes every existing property of an imported CID and
	// writes the snapshot's values verbatim. Untouched CIDs are left alone.
	ModeReplace Mode = "replace"
	// ModeMerge keeps unmentioned existing properties and only touches the
	// fields present in the snapshot. Conflict resolution within merge is
	// controlled by Conflict.
	ModeMerge Mode = "merge"
)

// Conflict picks a winner for fields present on both sides during merge.
type Conflict string

const (
	// ConflictMine keeps the existing (target) value when both sides have one.
	ConflictMine Conflict = "mine"
	// ConflictSource overwrites the existing value with the snapshot's value.
	ConflictSource Conflict = "source"
)

// ImportOptions controls how Import applies a snapshot.
type ImportOptions struct {
	Mode     Mode
	Conflict Conflict
	DryRun   bool
	// PluginOutputDir is the on-disk destination for cache/plugin-output/*
	// entries. When empty, plugin-output is silently skipped on import.
	PluginOutputDir string
}

// FileResult summarizes what happened (or would happen, in dry-run) for one CID.
type FileResult struct {
	HashID    string `json:"hashId"`
	Added     int    `json:"added"`     // new property keys written
	Updated   int    `json:"updated"`   // existing property keys whose value changed
	Unchanged int    `json:"unchanged"` // existing property keys whose value matched
	Kept      int    `json:"kept"`      // existing property keys preserved by ConflictMine
	Deleted   int    `json:"deleted"`   // existing property keys removed (replace mode)
	Error     string `json:"error,omitempty"`
}

// PluginOutputResult summarizes what happened to cache/plugin-output entries.
type PluginOutputResult struct {
	Written int `json:"written"`
	Skipped int `json:"skipped"`
}

// ImportResult is the response body for the import endpoint.
type ImportResult struct {
	Mode         Mode                `json:"mode"`
	Conflict     Conflict            `json:"conflict,omitempty"`
	DryRun       bool                `json:"dryRun"`
	TotalFiles   int                 `json:"totalFiles"`
	FilesOK      int                 `json:"filesOk"`
	FilesFailed  int                 `json:"filesFailed"`
	Files        []FileResult        `json:"files,omitempty"`
	PluginOutput *PluginOutputResult `json:"pluginOutput,omitempty"`
}

// Importer applies snapshot ZIPs to a live Redis store.
type Importer struct {
	storage *storage.Client
}

func NewImporter(stor *storage.Client) *Importer {
	return &Importer{storage: stor}
}

// Import reads a ZIP from r (which must support io.ReaderAt; callers pass
// *bytes.Reader after buffering the upload) and applies it to Redis.
func (i *Importer) Import(r io.ReaderAt, size int64, opts ImportOptions) (*ImportResult, error) {
	if i.storage == nil || !i.storage.IsConnected() {
		return nil, fmt.Errorf("storage not connected")
	}
	if opts.Mode != ModeReplace && opts.Mode != ModeMerge {
		return nil, fmt.Errorf("invalid mode %q (want replace|merge)", opts.Mode)
	}
	if opts.Mode == ModeMerge && opts.Conflict != ConflictMine && opts.Conflict != ConflictSource {
		return nil, fmt.Errorf("invalid conflict %q (want mine|source)", opts.Conflict)
	}

	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	var manifest Manifest
	if err := readJSON(zr, ManifestFile, &manifest); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if manifest.SchemaVersion > ManifestSchemaVersion {
		return nil, fmt.Errorf("snapshot schemaVersion %d exceeds supported %d",
			manifest.SchemaVersion, ManifestSchemaVersion)
	}
	if manifest.SchemaVersion < ManifestSchemaVersion {
		return nil, fmt.Errorf("snapshot schemaVersion %d is no longer supported (current: %d); v1 used content-hash root keys which are incompatible with the UUID-rooted schema",
			manifest.SchemaVersion, ManifestSchemaVersion)
	}

	result := &ImportResult{
		Mode:     opts.Mode,
		Conflict: opts.Conflict,
		DryRun:   opts.DryRun,
	}

	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, MetadataDir) || !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		hashID := strings.TrimSuffix(strings.TrimPrefix(f.Name, MetadataDir), ".json")
		if hashID == "" {
			continue
		}
		fr := i.applyOne(f, hashID, opts)
		result.TotalFiles++
		if fr.Error != "" {
			result.FilesFailed++
		} else {
			result.FilesOK++
		}
		result.Files = append(result.Files, fr)
	}

	// Plugin-output extraction. Only applied on real imports (not dry-run).
	// If the snapshot doesn't contain a cache/plugin-output/ section, this is
	// a no-op. If the destination is unset, we skip silently — the manifest's
	// pluginOutput flag tells the caller it was on the source side either way.
	if !opts.DryRun && opts.PluginOutputDir != "" && manifest.Includes.PluginOutput {
		// Existing files are kept under ConflictMine / replace-doesn't-apply,
		// overwritten under ConflictSource. Replace mode behaves like
		// "overwrite" for cache files (it's the destructive choice for
		// metadata; mirror that intent here).
		overwrite := opts.Mode == ModeReplace || opts.Conflict == ConflictSource
		written, skipped, err := extractPluginOutput(zr, opts.PluginOutputDir, overwrite)
		if err != nil {
			return result, fmt.Errorf("extract plugin-output: %w", err)
		}
		result.PluginOutput = &PluginOutputResult{Written: written, Skipped: skipped}
		log.Printf("[Snapshot] import: plugin-output written=%d skipped=%d", written, skipped)
	}

	log.Printf("[Snapshot] import: mode=%s conflict=%s dryRun=%v files=%d ok=%d failed=%d",
		opts.Mode, opts.Conflict, opts.DryRun, result.TotalFiles, result.FilesOK, result.FilesFailed)
	return result, nil
}

func (i *Importer) applyOne(f *zip.File, hashID string, opts ImportOptions) FileResult {
	fr := FileResult{HashID: hashID}

	rc, err := f.Open()
	if err != nil {
		fr.Error = fmt.Sprintf("open entry: %v", err)
		return fr
	}
	defer rc.Close()

	var nested interface{}
	if err := json.NewDecoder(rc).Decode(&nested); err != nil {
		fr.Error = fmt.Sprintf("decode json: %v", err)
		return fr
	}
	incoming := Flatten(nested)

	existing, err := i.storage.GetMetadataFlat(hashID)
	if err != nil {
		fr.Error = fmt.Sprintf("read existing: %v", err)
		return fr
	}
	if existing == nil {
		existing = map[string]string{}
	}

	switch opts.Mode {
	case ModeReplace:
		// Diff for reporting.
		toWrite := map[string]string{}
		for k, v := range incoming {
			toWrite[k] = v
			if cur, ok := existing[k]; ok {
				if cur == v {
					fr.Unchanged++
				} else {
					fr.Updated++
				}
			} else {
				fr.Added++
			}
		}
		for k := range existing {
			if _, ok := incoming[k]; !ok {
				fr.Deleted++
			}
		}
		if !opts.DryRun {
			if _, err := i.storage.DeleteMetadata(hashID); err != nil {
				fr.Error = fmt.Sprintf("delete existing: %v", err)
				return fr
			}
			if err := i.storage.SetMetadataFlat(hashID, toWrite); err != nil {
				fr.Error = fmt.Sprintf("write: %v", err)
				return fr
			}
		}

	case ModeMerge:
		toWrite := map[string]string{}
		for k, v := range incoming {
			cur, present := existing[k]
			switch {
			case !present:
				toWrite[k] = v
				fr.Added++
			case cur == v:
				fr.Unchanged++
			case opts.Conflict == ConflictSource:
				toWrite[k] = v
				fr.Updated++
			case opts.Conflict == ConflictMine:
				fr.Kept++
			}
		}
		if !opts.DryRun && len(toWrite) > 0 {
			if _, err := i.storage.MergeMetadataFlat(hashID, toWrite); err != nil {
				fr.Error = fmt.Sprintf("merge: %v", err)
				return fr
			}
		}
	}

	return fr
}

func readJSON(zr *zip.Reader, name string, out interface{}) error {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return json.NewDecoder(rc).Decode(out)
	}
	return fmt.Errorf("entry %q not found", name)
}
