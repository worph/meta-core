package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/metazla/meta-core/internal/watcher"
)

// registerFileRequest is the body of POST /api/files/register.
type registerFileRequest struct {
	// Path relative to FILES_PATH, e.g. "plugin/subtitles/ab12….eng.srt".
	Path string `json:"path"`
}

type registerFileResponse struct {
	Root    string `json:"root"`
	CID     string `json:"cid"`
	Created bool   `json:"created"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
}

// pluginRelPath normalises a FILES_PATH-relative path and refuses anything
// outside plugin/. Plugin output is the only tree a service may register this
// way: /files/watch is the user's library and belongs to the watcher.
func pluginRelPath(raw string) (string, bool) {
	p := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(raw)), "/")
	if p == "" {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := path.Clean(p)
	if !strings.HasPrefix(clean, "plugin/") || clean == "plugin/" {
		return "", false
	}
	return clean, true
}

// handleRegisterFile handles POST /api/files/register.
//
// Does for ONE file what the watcher does for every file it scans: midhash256
// it, mint a root carrying filePath/sizeByte/mtimeNano, register the
// cid:<midhash> alias, and append a file:events entry so meta-share's
// ipfs_seed chunks, seeds and provides it.
//
// It exists for cores that run with ENABLE_FILE_WATCHER=false (a meta-watch
// client box). Without it, bytes written under /files/plugin over WebDAV are
// never resolvable by CID and never reach the swarm. Idempotent: the same
// content at the same path returns the existing root with created=false.
func (s *Server) handleRegisterFile(w http.ResponseWriter, r *http.Request) {
	if s.storage == nil || !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	var req registerFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rel, ok := pluginRelPath(req.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "path must be a file under plugin/")
		return
	}

	fullPath := filepath.Join(s.config.FilesPath, filepath.FromSlash(rel))
	absFiles, _ := filepath.Abs(filepath.Join(s.config.FilesPath, "plugin"))
	absFull, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFull, absFiles+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "path must be a file under plugin/")
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory, not a file")
		return
	}

	midhash, err := watcher.ComputeMidHash256(fullPath, info.Size())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash file")
		return
	}

	root, created, err := s.storage.RegisterFileTuple(fullPath, info.Size(), info.ModTime().UnixNano(), midhash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if created {
		ev := watcher.FileEvent{
			Type:       watcher.EventTypeAdd,
			Path:       rel,
			Size:       info.Size(),
			MidHash256: midhash,
			Timestamp:  watcher.NowMS(),
		}
		if err := watcher.PublishEvent(s.storage, ev); err != nil {
			// The record is written and resolvable; only seeding is late.
			writeError(w, http.StatusInternalServerError, "registered, but file event not published: "+err.Error())
			return
		}
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, registerFileResponse{
		Root:    root,
		CID:     midhash,
		Created: created,
		Path:    rel,
		Size:    info.Size(),
	})
}
