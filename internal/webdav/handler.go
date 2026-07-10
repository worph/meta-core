package webdav

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/metazla/meta-core/internal/config"
	"golang.org/x/net/webdav"
)

// Handler handles WebDAV requests (caching is handled by nginx proxy_cache)
type Handler struct {
	config    *config.Config
	filesPath string
	webdavFS  webdav.Handler
}

// NewHandler creates a new WebDAV handler
func NewHandler(cfg *config.Config) *Handler {
	h := &Handler{
		config:    cfg,
		filesPath: cfg.FilesPath,
	}

	// Create standard WebDAV handler for write operations
	h.webdavFS = webdav.Handler{
		FileSystem: webdav.Dir(cfg.FilesPath),
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				log.Printf("[WebDAV] %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}

	return h
}

// ServeHTTP handles WebDAV requests
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove /webdav prefix to get the file path
	path := strings.TrimPrefix(r.URL.Path, "/webdav")
	if path == "" {
		path = "/"
	}

	switch r.Method {
	case "GET", "HEAD":
		h.handleRead(w, r, path)
	case "PUT":
		h.handlePut(w, r, path)
	case "DELETE":
		h.handleDelete(w, r, path)
	case "MKCOL":
		h.handleMkcol(w, r, path)
	case "COPY", "MOVE":
		h.handleCopyMove(w, r, path)
	case "PROPFIND":
		h.handlePropfind(w, r, path)
	case "OPTIONS":
		h.handleOptions(w, r)
	default:
		// Pass to standard WebDAV handler
		h.webdavFS.ServeHTTP(w, r)
	}
}

// handleRead handles GET and HEAD requests (caching is done by nginx proxy_cache)
func (h *Handler) handleRead(w http.ResponseWriter, r *http.Request, path string) {
	fullPath := filepath.Join(h.filesPath, path)

	// Check if it's a directory
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if info.IsDir() {
		// Return directory listing as JSON
		h.handleDirListing(w, r, fullPath, path)
		return
	}

	// Serve file directly (nginx handles caching via proxy_cache)
	http.ServeFile(w, r, fullPath)
}

// handleDirListing returns a directory listing as JSON
func (h *Handler) handleDirListing(w http.ResponseWriter, r *http.Request, fullPath, relPath string) {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type dirEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		MTime string `json:"mtime"`
		Size  int64  `json:"size,omitempty"`
	}

	var result []dirEntry
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		entryType := "file"
		if entry.IsDir() {
			entryType = "directory"
		}

		result = append(result, dirEntry{
			Name:  entry.Name(),
			Type:  entryType,
			MTime: info.ModTime().Format("2006-01-02T15:04:05Z"),
			Size:  info.Size(),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handlePut handles PUT requests (file upload)
func (h *Handler) handlePut(w http.ResponseWriter, r *http.Request, path string) {
	fullPath := filepath.Join(h.filesPath, path)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create/overwrite file
	file, err := os.Create(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Copy request body to file
	_, err = io.Copy(file, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Note: nginx proxy_cache handles invalidation via TTL (1h inactive)
	w.WriteHeader(http.StatusCreated)
}

// handleDelete handles DELETE requests
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request, path string) {
	fullPath := filepath.Join(h.filesPath, path)

	// Check if exists
	_, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Remove file or directory
	if err := os.RemoveAll(fullPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Note: nginx proxy_cache handles invalidation via TTL (1h inactive)
	w.WriteHeader(http.StatusNoContent)
}

// handleMkcol handles MKCOL requests (create directory)
func (h *Handler) handleMkcol(w http.ResponseWriter, r *http.Request, path string) {
	fullPath := filepath.Join(h.filesPath, path)

	// Check if already exists
	if _, err := os.Stat(fullPath); err == nil {
		http.Error(w, "Already exists", http.StatusMethodNotAllowed)
		return
	}

	// Create directory
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// handleCopyMove handles COPY and MOVE requests
func (h *Handler) handleCopyMove(w http.ResponseWriter, r *http.Request, path string) {
	// Get destination from header
	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "Destination header required", http.StatusBadRequest)
		return
	}

	// Parse destination URL to get path
	destPath := strings.TrimPrefix(dest, "/webdav")
	if strings.HasPrefix(dest, "http") {
		// Full URL provided - extract path
		parts := strings.SplitN(dest, "/webdav", 2)
		if len(parts) == 2 {
			destPath = parts[1]
		}
	}

	srcFullPath := filepath.Join(h.filesPath, path)
	destFullPath := filepath.Join(h.filesPath, destPath)

	// Check if source exists
	if _, err := os.Stat(srcFullPath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Source not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destFullPath), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Method == "MOVE" {
		// Move file
		if err := os.Rename(srcFullPath, destFullPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Note: nginx proxy_cache handles invalidation via TTL (1h inactive)
	} else {
		// Copy file
		if err := copyFile(srcFullPath, destFullPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusCreated)
}

// handlePropfind handles PROPFIND requests
func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request, path string) {
	fullPath := filepath.Join(h.filesPath, path)

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}

	// Build XML response
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n"))
	w.Write([]byte(`<D:multistatus xmlns:D="DAV:">` + "\n"))

	// Add entry for the requested resource
	h.writePropfindEntry(w, path, info)

	// If directory and depth > 0, add children
	if info.IsDir() && depth != "0" {
		entries, err := os.ReadDir(fullPath)
		if err == nil {
			for _, entry := range entries {
				childInfo, err := entry.Info()
				if err != nil {
					continue
				}
				childPath := filepath.Join(path, entry.Name())
				h.writePropfindEntry(w, childPath, childInfo)
			}
		}
	}

	w.Write([]byte(`</D:multistatus>` + "\n"))
}

// writePropfindEntry writes a single PROPFIND response entry
func (h *Handler) writePropfindEntry(w http.ResponseWriter, path string, info os.FileInfo) {
	href := "/webdav" + path
	if info.IsDir() && !strings.HasSuffix(href, "/") {
		href += "/"
	}

	resourceType := ""
	if info.IsDir() {
		resourceType = "<D:collection/>"
	}

	w.Write([]byte(`  <D:response>` + "\n"))
	w.Write([]byte(`    <D:href>` + href + `</D:href>` + "\n"))
	w.Write([]byte(`    <D:propstat>` + "\n"))
	w.Write([]byte(`      <D:prop>` + "\n"))
	w.Write([]byte(`        <D:resourcetype>` + resourceType + `</D:resourcetype>` + "\n"))
	if !info.IsDir() {
		w.Write([]byte(`        <D:getcontentlength>` + formatInt64(info.Size()) + `</D:getcontentlength>` + "\n"))
	}
	w.Write([]byte(`        <D:getlastmodified>` + info.ModTime().Format("Mon, 02 Jan 2006 15:04:05 GMT") + `</D:getlastmodified>` + "\n"))
	w.Write([]byte(`      </D:prop>` + "\n"))
	w.Write([]byte(`      <D:status>HTTP/1.1 200 OK</D:status>` + "\n"))
	w.Write([]byte(`    </D:propstat>` + "\n"))
	w.Write([]byte(`  </D:response>` + "\n"))
}

// handleOptions handles OPTIONS requests
func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND")
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// formatInt64 formats an int64 as a string
func formatInt64(n int64) string {
	return strconv.FormatInt(n, 10)
}
