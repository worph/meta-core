package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/metazla/meta-core/internal/snapshot"
)

// Bound the upload size so a malicious client can't OOM the server.
// 2 GiB is well above any realistic export.
const maxImportBytes = 2 << 30

// pluginOutputDir is where plugin-produced artifacts live on disk
// (subtitles, posters, etc). Mounted from the host into the meta-core
// container at $FILES_PATH/plugin.
func (s *Server) pluginOutputDir() string {
	return filepath.Join(s.config.FilesPath, "plugin")
}

// handleSnapshotExport streams a ZIP snapshot of all metadata.
// GET /api/snapshot/export?include=hash-index,plugin-output
func (s *Server) handleSnapshotExport(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// The server has a 30s WriteTimeout (server.go:222). Long exports
	// blow past that, leaving a truncated ZIP without a central directory.
	// Clear the deadline for this handler so the export can run as long as
	// needed. ResponseController is the standard way to do this.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	include := splitCSVParam(r.URL.Query().Get("include"))
	opts := snapshot.ExportOptions{
		IncludePluginOutput: include["plugin-output"],
		PluginOutputDir:     s.pluginOutputDir(),
	}

	filename := fmt.Sprintf("metamesh-snapshot-%s.zip", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	// We stream — no Content-Length.

	exporter := snapshot.NewExporter(s.storage)
	if _, err := exporter.Export(w, opts); err != nil {
		// Headers may already be sent; log and bail.
		// Client will see a truncated zip, which is the best signal we can give.
		fmt.Printf("[Snapshot] export failed: %v\n", err)
		return
	}
}

// handleSnapshotImport applies an uploaded ZIP snapshot.
// POST /api/snapshot/import?mode=replace|merge&conflict=mine|source&dry_run=true
// Body: application/zip (raw) OR multipart/form-data with field "snapshot"
func (s *Server) handleSnapshotImport(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Long imports can blow past the server's 30s ReadTimeout/WriteTimeout
	// when the snapshot is large. Clear both for this handler.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	q := r.URL.Query()
	opts := snapshot.ImportOptions{
		Mode:            snapshot.Mode(q.Get("mode")),
		Conflict:        snapshot.Conflict(q.Get("conflict")),
		DryRun:          strings.EqualFold(q.Get("dry_run"), "true"),
		PluginOutputDir: s.pluginOutputDir(),
	}
	if opts.Mode == "" {
		opts.Mode = snapshot.ModeMerge
	}
	if opts.Mode == snapshot.ModeMerge && opts.Conflict == "" {
		opts.Conflict = snapshot.ConflictSource
	}

	body, err := readImportBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	importer := snapshot.NewImporter(s.storage)
	result, err := importer.Import(bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// handleSnapshotWipe clears Redis metadata.
// POST /api/snapshot/wipe?scope=metadata
func (s *Server) handleSnapshotWipe(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	scopes := splitCSVParam(r.URL.Query().Get("scope"))
	if len(scopes) == 0 {
		writeError(w, http.StatusBadRequest, "scope query param is required (e.g. scope=metadata)")
		return
	}
	scope := snapshot.WipeScope{
		Metadata: scopes["metadata"],
	}
	if !scope.Metadata {
		writeError(w, http.StatusBadRequest, "no supported scope requested; scope=metadata is the only accepted value")
		return
	}

	wiper := snapshot.NewWiper(s.storage)
	res, err := wiper.Wipe(scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// readImportBody accepts either a raw application/zip body or a multipart
// upload with a field named "snapshot". Buffers up to maxImportBytes.
func readImportBody(r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxImportBytes)

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			return nil, fmt.Errorf("parse multipart: %w", err)
		}
		f, _, err := r.FormFile("snapshot")
		if err != nil {
			return nil, fmt.Errorf("missing 'snapshot' file field: %w", err)
		}
		defer f.Close()
		return io.ReadAll(f)
	}

	return io.ReadAll(r.Body)
}

// splitCSVParam parses "a,b,c" into a set. Empty values yield an empty set.
func splitCSVParam(v string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			out[p] = true
		}
	}
	return out
}
