package api

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
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
	Role      string                 `json:"role"`
	Redis     bool                   `json:"redis"`
	Timestamp string                 `json:"timestamp"`
	Leader    *leader.LeaderLockInfo `json:"leader,omitempty"`
}

// StatusResponse is the response for /status
type StatusResponse struct {
	Status      string                 `json:"status"`
	Role        string                 `json:"role"`
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

// ErrorResponse is the response for errors
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

var startTime = time.Now()

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status:    "ok",
		Role:      string(s.election.Role()),
		Redis:     s.storage.Health(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Leader:    s.election.LeaderInfo(),
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
		Role:        string(s.election.Role()),
		Redis:       s.storage.Health(),
		ServiceName: s.config.ServiceName,
		Version:     s.config.ServiceVersion,
		Uptime:      int64(time.Since(startTime).Seconds()),
		FileCount:   fileCount,
		Leader:      s.election.LeaderInfo(),
	}

	if !response.Redis {
		response.Status = "degraded"
	}

	writeJSON(w, http.StatusOK, response)
}

// handleLeader handles GET /leader
func (s *Server) handleLeader(w http.ResponseWriter, r *http.Request) {
	info := s.election.LeaderInfo()
	if info == nil {
		writeError(w, http.StatusServiceUnavailable, "no leader available")
		return
	}

	writeJSON(w, http.StatusOK, info)
}

// handleRole handles GET /role
func (s *Server) handleRole(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"role": string(s.election.Role()),
	})
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

	var metadata map[string]string
	if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
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
		writeError(w, http.StatusNotFound, "file not found for CID")
		return
	}

	// Construct full path
	fullPath := filepath.Join(s.config.FilesPath, relPath)

	// Check if file exists
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file does not exist on disk")
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

// handleListServices handles GET /services
func (s *Server) handleListServices(w http.ResponseWriter, r *http.Request) {
	services, err := s.discovery.DiscoverAll()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"services": services,
		"count":    len(services),
	})
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

// writeJSON writes a JSON response
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes an error response
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, ErrorResponse{
		Error:   http.StatusText(status),
		Message: message,
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

	var metadata map[string]string
	if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
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

// handlePutProperty handles PUT /meta/{hash}/{key}
// Sets a single property value
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

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Read body as plain text
	buf := make([]byte, 1024*1024) // 1MB max
	n, err := r.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	value := string(buf[:n])

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

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Read body as plain text
	buf := make([]byte, 1024*1024) // 1MB max
	n, err := r.Body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	value := string(buf[:n])

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
