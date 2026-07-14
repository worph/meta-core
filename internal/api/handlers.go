package api

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/metazla/meta-core/internal/leader"
)

// contentTypeByExt maps file extensions to MIME types
var contentTypeByExt = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".gif":  "image/gif",
	".mp4":  "video/mp4",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".ts":   "video/mp2t",
	".m3u8": "application/vnd.apple.mpegurl",
}

// HealthResponse is the response for /health
type HealthResponse struct {
	Status    string                 `json:"status"`
	Redis     bool                   `json:"redis"`
	Timestamp string                 `json:"timestamp"`
	Leader    *leader.LeaderLockInfo `json:"leader,omitempty"`
}

// StatusResponse is the response for /status
type StatusResponse struct {
	Status      string                 `json:"status"`
	Redis       bool                   `json:"redis"`
	ServiceName string                 `json:"serviceName"`
	Version     string                 `json:"version"`
	Uptime      int64                  `json:"uptimeSeconds"`
	FileCount   int                    `json:"fileCount"`
	Leader      *leader.LeaderLockInfo `json:"leader,omitempty"`
}

// MetadataResponse is the response for /meta/{hash}
type MetadataResponse struct {
	HashID   string            `json:"hashId"`
	Metadata map[string]string `json:"metadata"`
}

// DataPathResponse is the response for /data/{hash}/path
type DataPathResponse struct {
	HashID string `json:"hashId"`
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// ErrorResponse is the typed error envelope returned on every 4xx/5xx.
//
// Shape and slug vocabulary documented in docs/api-mediated-access.md
// ("Error envelope"). Stable slug strings let clients switch on the failure
// category without parsing the message; `retryable` tells callers whether
// a backoff-and-retry has any chance of succeeding.
type ErrorResponse struct {
	Error     string `json:"error"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable"`
}

// Stable error-slug strings. Pinned in the doc; consumers may match on
// these. Do not rename without a deprecation cycle.
const (
	ErrAliasCollision    = "alias_collision"
	ErrUnknownRoot       = "unknown_root"
	ErrUnknownCID        = "unknown_cid"
	ErrSchemaViolation   = "schema_violation"
	ErrStorageUnavailable = "storage_unavailable"
	ErrInternal          = "internal"
	ErrDeprecatedField   = "deprecated_field"
)

// rejectDeprecatedField reports whether a field name is one of the removed
// per-algorithm CID shapes (`cid_sha2-256`, `cid_midhash256`, `midhash256`, …).
//
// This is a tripwire, not a compatibility shim. Such a field used to be
// accepted and written verbatim — but the reverse-index hook
// (`maybeAddAliasFromFieldLocked`) only registers aliases for fields named
// `cids/<cid>`, so a `cid_*` field was stored and then NEVER INDEXED: the
// record silently became unresolvable by that CID. Failing the write loudly is
// strictly better than a record that looks fine and cannot be found.
//
// Writers must emit the bare-CID key-set instead: `cids/<bareCid>` = "true".
// See METADATA_KEYS.md §2/§14.13.
func rejectDeprecatedField(field string) bool {
	return strings.HasPrefix(field, "cid_") ||
		field == "midhash256" ||
		field == "canonical_cid"
}

// validateFields rejects a write carrying any deprecated CID field. Returns
// true if the request was rejected (the handler must return immediately).
func validateFields(w http.ResponseWriter, metadata map[string]string) bool {
	for field := range metadata {
		if rejectDeprecatedField(field) {
			writeErrorSlug(w, http.StatusBadRequest, ErrDeprecatedField,
				fmt.Sprintf("field %q is a removed per-algorithm CID shape; "+
					"write the bare-CID key-set instead: \"cids/<bareCid>\": \"true\" "+
					"(a cid_* field would be stored but never indexed, leaving the "+
					"record unresolvable by that CID). See METADATA_KEYS.md §2/§14.13.",
					field),
				false)
			return true
		}
	}
	return false
}

// defaultSlugFor maps an HTTP status code to the most likely error slug.
// Handlers that want to override (e.g. /api/meta/{cid} → "unknown_cid"
// instead of "unknown_root" on 404) call writeErrorSlug directly.
func defaultSlugFor(status int) (slug string, retryable bool) {
	switch status {
	case http.StatusConflict:
		return ErrAliasCollision, false
	case http.StatusNotFound:
		return ErrUnknownRoot, false
	case http.StatusBadRequest:
		return ErrSchemaViolation, false
	case http.StatusServiceUnavailable:
		return ErrStorageUnavailable, true
	case http.StatusInternalServerError:
		return ErrInternal, true
	default:
		// 4xx → not retryable, 5xx → retryable. Conservative default.
		return ErrInternal, status >= 500
	}
}

var startTime = time.Now()

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status:    "ok",
		Redis:     s.storage.Health(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Leader:    s.leaderProvider.LeaderInfo(),
	}

	if !response.Redis {
		response.Status = "degraded"
	}

	writeJSON(w, http.StatusOK, response)
}

// handleStatus handles GET /status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	fileCount := 0
	if s.storage.IsConnected() {
		count, _ := s.storage.CountFiles()
		fileCount = count
	}

	response := StatusResponse{
		Status:      "ok",
		Redis:       s.storage.Health(),
		ServiceName: s.config.ServiceName,
		Version:     s.config.ServiceVersion,
		Uptime:      int64(time.Since(startTime).Seconds()),
		FileCount:   fileCount,
		Leader:      s.leaderProvider.LeaderInfo(),
	}

	if !response.Redis {
		response.Status = "degraded"
	}

	writeJSON(w, http.StatusOK, response)
}

// handleLeader handles GET /leader
func (s *Server) handleLeader(w http.ResponseWriter, r *http.Request) {
	info := s.leaderProvider.LeaderInfo()
	if info == nil {
		writeError(w, http.StatusServiceUnavailable, "no leader available")
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// URLsResponse is the response for /urls
type URLsResponse struct {
	Hostname          string `json:"hostname"`
	BaseUrl           string `json:"baseUrl"`
	ApiUrl            string `json:"apiUrl"`
	RedisUrl          string `json:"redisUrl"`
	WebdavUrl         string `json:"webdavUrl"`
	WebdavUrlInternal string `json:"webdavUrlInternal"`
}

// handleURLs handles GET /urls
// Returns URLs for this instance (always leader since Go binary only runs as leader)
func (s *Server) handleURLs(w http.ResponseWriter, r *http.Request) {
	info := s.leaderProvider.LeaderInfo()
	if info == nil {
		writeError(w, http.StatusServiceUnavailable, "no leader available")
		return
	}

	response := URLsResponse{
		Hostname:          info.Hostname,
		BaseUrl:           info.BaseUrl,
		ApiUrl:            info.ApiUrl,
		RedisUrl:          info.RedisUrl,
		WebdavUrl:         info.WebdavUrl,
		WebdavUrlInternal: info.WebdavUrlInternal,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleGetMeta handles GET /meta/{hash}
func (s *Server) handleGetMeta(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Resolve CID → UUID via the reverse index so reads find data wherever
	// the watcher minted the root, regardless of which CID the caller knows.
	hashID = s.storage.ResolveRoot(hashID)

	metadata, err := s.storage.GetMetadataFlat(hashID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if metadata == nil {
		writeError(w, http.StatusNotFound, "metadata not found")
		return
	}

	response := MetadataResponse{
		HashID:   hashID,
		Metadata: metadata,
	}

	writeJSON(w, http.StatusOK, response)
}

// handlePutMeta handles PUT /meta/{hash}
func (s *Server) handlePutMeta(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// CID → UUID resolution so writes land on the canonical root, not on
	// a parallel midhash-rooted entry the watcher's UUID never sees.
	hashID = s.storage.ResolveRoot(hashID)

	var metadata map[string]string
	if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if validateFields(w, metadata) {
		return
	}

	if err := s.storage.SetMetadataFlat(hashID, metadata); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"hashId":  hashID,
	})
}

// handleDeleteMeta handles DELETE /meta/{hash}
func (s *Server) handleDeleteMeta(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashID = s.storage.ResolveRoot(hashID)

	deleted, err := s.storage.DeleteMetadata(hashID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"hashId":  hashID,
		"deleted": deleted,
	})
}

// handleListMeta handles GET /meta
func (s *Server) handleListMeta(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hashIds": hashIDs,
		"count":   len(hashIDs),
	})
}

// handleGetDataPath handles GET /data/{hash}/path
func (s *Server) handleGetDataPath(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Get the file path from metadata
	filePath, err := s.storage.GetProperty(hashID, "filePath")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if filePath == "" {
		writeError(w, http.StatusNotFound, "file path not found")
		return
	}

	// Check if file exists
	fullPath := s.config.FilesPath + "/" + filePath
	_, statErr := os.Stat(fullPath)
	exists := statErr == nil

	response := DataPathResponse{
		HashID: hashID,
		Path:   fullPath,
		Exists: exists,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleHeadData handles HEAD /data/{hash}
func (s *Server) handleHeadData(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if !s.storage.IsConnected() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// Get the file path from metadata
	filePath, err := s.storage.GetProperty(hashID, "filePath")
	if err != nil || filePath == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Check if file exists
	fullPath := s.config.FilesPath + "/" + filePath
	if _, err := os.Stat(fullPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleGetFileByCIDInfo handles GET /api/file/{cid}/info.
// Returns lightweight metadata for the file (existence, content type, size,
// relative path) without streaming any bytes. Used by the editor to decide
// whether to render a preview (image/video) or a download-only button.
func (s *Server) handleGetFileByCIDInfo(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	cid := vars["cid"]

	type infoResp struct {
		Exists      bool   `json:"exists"`
		ContentType string `json:"contentType,omitempty"`
		Size        int64  `json:"size,omitempty"`
		FilePath    string `json:"filePath,omitempty"`
	}

	w.Header().Set("Content-Type", "application/json")

	if cid == "" {
		_ = json.NewEncoder(w).Encode(infoResp{Exists: false})
		return
	}
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	relPath, err := s.storage.LookupPathByCID(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if relPath == "" {
		_ = json.NewEncoder(w).Encode(infoResp{Exists: false})
		return
	}

	fullPath := filepath.Join(s.config.FilesPath, relPath)
	fi, err := os.Stat(fullPath)
	if err != nil {
		_ = json.NewEncoder(w).Encode(infoResp{Exists: false, FilePath: relPath})
		return
	}

	ext := strings.ToLower(filepath.Ext(fullPath))
	contentType, ok := contentTypeByExt[ext]
	if !ok {
		contentType = "application/octet-stream"
	}

	_ = json.NewEncoder(w).Encode(infoResp{
		Exists:      true,
		ContentType: contentType,
		Size:        fi.Size(),
		FilePath:    relPath,
	})
}

// handleGetFileByCID handles GET /file/{cid}
// Serves a file by looking up its CID in poster/backdrop metadata
func (s *Server) handleGetFileByCID(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	cid := vars["cid"]

	if cid == "" {
		writeError(w, http.StatusBadRequest, "cid is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Look up the file path by CID
	relPath, err := s.storage.LookupPathByCID(cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if relPath == "" {
		writeErrorSlug(w, http.StatusNotFound, ErrUnknownCID, "file not found for CID", false)
		return
	}

	// Construct full path
	fullPath := filepath.Join(s.config.FilesPath, relPath)

	// Check if file exists
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		writeErrorSlug(w, http.StatusNotFound, ErrUnknownCID, "file does not exist on disk", false)
		return
	}

	// Determine content type from extension
	ext := strings.ToLower(filepath.Ext(fullPath))
	contentType, ok := contentTypeByExt[ext]
	if !ok {
		contentType = "application/octet-stream"
	}

	// Open the file
	file, err := os.Open(fullPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer file.Close()

	// Set content type header
	w.Header().Set("Content-Type", contentType)

	// Serve the file (supports range requests)
	http.ServeContent(w, r, fileInfo.Name(), fileInfo.ModTime(), file)
}

// handleListServices handles GET /services and GET /api/services
// Supports optional ?current=<service-name> query parameter
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	services, err := s.discovery.DiscoverAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response := map[string]interface{}{
		"services": services,
		"count":    len(services),
	}

	// Add 'current' field if query parameter is provided
	current := r.URL.Query().Get("current")
	if current != "" {
		response["current"] = current
	}

	writeJSON(w, http.StatusOK, response)
}

// handleGetService handles GET /services/{name}
func (s *Server) handleGetService(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	if name == "" {
		writeError(w, http.StatusBadRequest, "service name is required")
		return
	}

	service, err := s.discovery.Discover(name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if service == nil {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}

	writeJSON(w, http.StatusOK, service)
}

// handleCleanupStats handles GET /services/cleanup/stats
func (s *Server) handleCleanupStats(w http.ResponseWriter, r *http.Request) {
	if s.cleaner == nil {
		writeError(w, http.StatusServiceUnavailable, "cleaner not initialized")
		return
	}

	stats := s.cleaner.Stats()
	writeJSON(w, http.StatusOK, stats)
}

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes the typed error envelope. The slug + retryable bit
// are inferred from the status (see defaultSlugFor); call writeErrorSlug
// directly when a more specific slug applies.
func writeError(w http.ResponseWriter, status int, message string) {
	slug, retryable := defaultSlugFor(status)
	writeJSON(w, status, ErrorResponse{
		Error:     slug,
		Message:   message,
		Retryable: retryable,
	})
}

// writeErrorSlug writes the typed envelope with an explicit slug and
// retryable bit. Use this for endpoint-specific slugs (alias_collision,
// unknown_cid) where the default mapping from HTTP status would be wrong.
func writeErrorSlug(w http.ResponseWriter, status int, slug, message string, retryable bool) {
	writeJSON(w, status, ErrorResponse{
		Error:     slug,
		Message:   message,
		Retryable: retryable,
	})
}

// handlePatchMeta handles PATCH /meta/{hash}
// Merges the provided metadata into existing (does not delete missing keys)
func (s *Server) handlePatchMeta(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// PATCH is the hot write path for meta-sort. Resolve so plugin output
	// merges into the watcher's UUID root rather than minting a parallel
	// midhash-rooted entry.
	hashID = s.storage.ResolveRoot(hashID)

	var metadata map[string]string
	if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if validateFields(w, metadata) {
		return
	}

	updated, err := s.storage.MergeMetadataFlat(hashID, metadata)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"hashId":  hashID,
		"updated": updated,
	})
}

// handleGetProperty handles GET /meta/{hash}/{key}
// Gets a single property value
func (s *Server) handleGetProperty(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]
	key := vars["key"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashID = s.storage.ResolveRoot(hashID)

	value, err := s.storage.GetProperty(hashID, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if value == "" {
		writeError(w, http.StatusNotFound, "property not found")
		return
	}

	// Return plain text for single property
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(value))
}

// PropertyBody is the request body for PUT /meta/{hash}/{key}
type PropertyBody struct {
	Value string `json:"value"`
}

// handlePutProperty handles PUT /meta/{hash}/{key}
// Sets a single property value
// Accepts JSON body with {"value": "..."} or plain text
func (s *Server) handlePutProperty(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]
	key := vars["key"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if validateFields(w, map[string]string{key: ""}) {
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashID = s.storage.ResolveRoot(hashID)

	// Read body
	buf := make([]byte, 1024*1024) // 1MB max
	n, err := r.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	body := buf[:n]

	// Try to parse as JSON {"value": "..."} first
	var propBody PropertyBody
	var value string
	if err := json.Unmarshal(body, &propBody); err == nil && propBody.Value != "" {
		// JSON body with value field
		value = propBody.Value
	} else {
		// Plain text body
		value = string(body)
	}

	if err := s.storage.SetProperty(hashID, key, value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"hashId":   hashID,
		"property": key,
	})
}

// handleDeleteProperty handles DELETE /meta/{hash}/{key}
// Deletes a single property
func (s *Server) handleDeleteProperty(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]
	key := vars["key"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashID = s.storage.ResolveRoot(hashID)

	if err := s.storage.DeleteProperty(hashID, key); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"hashId":   hashID,
		"property": key,
	})
}

// handleAddToSet handles POST /meta/{hash}/_add/{key}
// Adds a value to a set-type field
// Accepts JSON body with {"value": "..."} or plain text
func (s *Server) handleAddToSet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hash"]
	key := vars["key"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash is required")
		return
	}

	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if validateFields(w, map[string]string{key: ""}) {
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashID = s.storage.ResolveRoot(hashID)

	// Read body
	buf := make([]byte, 1024*1024) // 1MB max
	n, err := r.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	body := buf[:n]

	// Try to parse as JSON {"value": "..."} first
	var propBody PropertyBody
	var value string
	if err := json.Unmarshal(body, &propBody); err == nil && propBody.Value != "" {
		// JSON body with value field
		value = propBody.Value
	} else {
		// Plain text body
		value = string(body)
	}

	if value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	added, err := s.storage.AddToSet(hashID, key, value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  true,
		"hashId":   hashID,
		"property": key,
		"added":    added,
	})
}

// CIDRequest is the request body for POST /file/cid
type CIDRequest struct {
	Path string `json:"path"`
}

// CIDResponse is the response for POST /file/cid
type CIDResponse struct {
	CID  string `json:"cid"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// handleComputeFileCID handles POST /file/cid
// Computes the IPFS-compatible CIDv1 (sha256) for a file
func (s *Server) handleComputeFileCID(w http.ResponseWriter, r *http.Request) {
	var req CIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Construct full path - path should be relative to FILES_PATH
	fullPath := filepath.Join(s.config.FilesPath, req.Path)

	// Security check: ensure path is within FILES_PATH
	absFilesPath, _ := filepath.Abs(s.config.FilesPath)
	absFullPath, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFullPath, absFilesPath) {
		writeError(w, http.StatusBadRequest, "path must be within files directory")
		return
	}

	// Check if file exists
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}

	if fileInfo.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory, not a file")
		return
	}

	// Open and hash the file
	file, err := os.Open(fullPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer file.Close()

	// Compute SHA-256 hash
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file")
		return
	}
	hashBytes := hasher.Sum(nil)

	// Build CIDv1 with raw codec (0x55) and sha256 multihash
	// CIDv1 format: version (0x01) + codec (0x55) + multihash
	// Multihash format: hash-code (0x12) + length (0x20) + hash
	cidBytes := make([]byte, 0, 2+2+32)
	cidBytes = append(cidBytes, 0x01)       // CIDv1
	cidBytes = append(cidBytes, 0x55)       // raw codec
	cidBytes = append(cidBytes, 0x12)       // sha256 code
	cidBytes = append(cidBytes, 0x20)       // 32 bytes
	cidBytes = append(cidBytes, hashBytes...)

	// Encode as base32lower with 'b' prefix (multibase)
	cid := "b" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(cidBytes))

	writeJSON(w, http.StatusOK, CIDResponse{
		CID:  cid,
		Path: req.Path,
		Size: fileInfo.Size(),
	})
}
