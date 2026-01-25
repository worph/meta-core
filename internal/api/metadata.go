package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

// HashIDsResponse is the response for GET /api/metadata/hash-ids
type HashIDsResponse struct {
	HashIds []string `json:"hashIds"`
	Count   int      `json:"count"`
}

// MetadataListItem represents a file in the metadata list
type MetadataListItem struct {
	HashID   string `json:"hashId"`
	Title    string `json:"title,omitempty"`
	FileName string `json:"fileName,omitempty"`
	FilePath string `json:"filePath,omitempty"`
	Type     string `json:"type,omitempty"`
	Year     string `json:"year,omitempty"`
}

// MetadataListResponse is the response for GET /api/metadata/list
type MetadataListResponse struct {
	Items []MetadataListItem `json:"items"`
	Count int                `json:"count"`
	Total int                `json:"total"`
}

// MetadataSearchRequest is the request body for POST /api/metadata/search
type MetadataSearchRequest struct {
	Query         string `json:"query,omitempty"`
	HashID        string `json:"hashId,omitempty"`
	Property      string `json:"property,omitempty"`
	PropertyValue string `json:"propertyValue,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

// MetadataSearchResult is a single search result
type MetadataSearchResult struct {
	HashID   string            `json:"hashId"`
	Metadata map[string]string `json:"metadata"`
}

// MetadataSearchResponse is the response for POST /api/metadata/search
type MetadataSearchResponse struct {
	Results []MetadataSearchResult `json:"results"`
	Count   int                    `json:"count"`
	Total   int                    `json:"total"`
}

// MetadataBatchUpdateRequest is the request body for POST /api/metadata/batch
type MetadataBatchUpdateRequest struct {
	Updates []MetadataBatchItem `json:"updates"`
}

// MetadataBatchItem is a single item in a batch update
type MetadataBatchItem struct {
	HashID     string            `json:"hashId"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Properties map[string]string `json:"properties,omitempty"`
}

// MetadataBatchResult is a single result from a batch update
type MetadataBatchResult struct {
	HashID string `json:"hashId"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// MetadataBatchResponse is the response for POST /api/metadata/batch
type MetadataBatchResponse struct {
	Status  string                `json:"status"`
	Total   int                   `json:"total"`
	Success int                   `json:"success"`
	Errors  int                   `json:"errors"`
	Results []MetadataBatchResult `json:"results"`
}

// PropertyUpdateRequest is the request body for PUT /api/metadata/{hashId}/property
type PropertyUpdateRequest struct {
	Property string `json:"property"`
	Value    string `json:"value"`
}

// handleGetHashIds handles GET /api/metadata/hash-ids
func (s *Server) handleGetHashIds(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	hashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response := HashIDsResponse{
		HashIds: hashIDs,
		Count:   len(hashIDs),
	}

	writeJSON(w, http.StatusOK, response)
}

// handleListMetadata handles GET /api/metadata/list
func (s *Server) handleListMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	// Parse pagination parameters
	offsetStr := r.URL.Query().Get("offset")
	limitStr := r.URL.Query().Get("limit")

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	// Get all hash IDs
	allHashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	total := len(allHashIDs)

	// Apply pagination
	end := offset + limit
	if end > total {
		end = total
	}

	var items []MetadataListItem
	if offset < total {
		for _, hashID := range allHashIDs[offset:end] {
			metadata, err := s.storage.GetMetadataFlat(hashID)
			if err != nil || metadata == nil {
				continue
			}

			item := MetadataListItem{
				HashID:   hashID,
				Title:    metadata["title"],
				FileName: metadata["fileName"],
				FilePath: metadata["filePath"],
				Type:     metadata["type"],
				Year:     metadata["year"],
			}
			items = append(items, item)
		}
	}

	response := MetadataListResponse{
		Items: items,
		Count: len(items),
		Total: total,
	}

	writeJSON(w, http.StatusOK, response)
}

// handleGetMetadataByHashId handles GET /api/metadata/{hashId}
func (s *Server) handleGetMetadataByHashId(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hashId"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash ID is required")
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

// handleUpdateMetadataByHashId handles PUT /api/metadata/{hashId}
func (s *Server) handleUpdateMetadataByHashId(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hashId"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash ID is required")
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

	// Exclude internal processing status field
	delete(metadata, "processingStatus")

	if err := s.storage.SetMetadataFlat(hashID, metadata); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"hashId":  hashID,
		"message": "Metadata updated successfully",
	})
}

// handleDeleteMetadataByHashId handles DELETE /api/metadata/{hashId}
func (s *Server) handleDeleteMetadataByHashId(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hashId"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash ID is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	deletedCount, err := s.storage.DeleteMetadata(hashID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"hashId":      hashID,
		"deletedKeys": deletedCount,
	})
}

// handleMetadataGetProperty handles GET /api/metadata/{hashId}/property?property=X
func (s *Server) handleMetadataGetProperty(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hashId"]
	property := r.URL.Query().Get("property")

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash ID is required")
		return
	}

	if property == "" {
		writeError(w, http.StatusBadRequest, "property parameter is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	value, err := s.storage.GetProperty(hashID, property)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if value == "" {
		writeError(w, http.StatusNotFound, "property not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hashId":   hashID,
		"property": property,
		"value":    value,
	})
}

// handleMetadataUpdateProperty handles PUT /api/metadata/{hashId}/property
func (s *Server) handleMetadataUpdateProperty(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hashID := vars["hashId"]

	if hashID == "" {
		writeError(w, http.StatusBadRequest, "hash ID is required")
		return
	}

	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	var req PropertyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Property == "" {
		writeError(w, http.StatusBadRequest, "property is required")
		return
	}

	if err := s.storage.SetProperty(hashID, req.Property, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "ok",
		"hashId":   hashID,
		"property": req.Property,
		"value":    req.Value,
		"message":  "Property updated successfully",
	})
}

// handleSearchMetadata handles POST /api/metadata/search
func (s *Server) handleSearchMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	var req MetadataSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Limit == 0 {
		req.Limit = 100
	}

	// If hashId is provided, do exact match
	if req.HashID != "" {
		metadata, err := s.storage.GetMetadataFlat(req.HashID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if metadata != nil {
			writeJSON(w, http.StatusOK, MetadataSearchResponse{
				Results: []MetadataSearchResult{{HashID: req.HashID, Metadata: metadata}},
				Count:   1,
				Total:   1,
			})
			return
		}
		writeJSON(w, http.StatusOK, MetadataSearchResponse{
			Results: []MetadataSearchResult{},
			Count:   0,
			Total:   0,
		})
		return
	}

	// Get all hash IDs
	allHashIDs, err := s.storage.GetAllHashIDs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var results []MetadataSearchResult
	total := len(allHashIDs)

	// Search fields for query
	searchFields := []string{"title", "fileName", "originaltitle", "showtitle", "filePath"}

	for _, hashID := range allHashIDs {
		if len(results) >= req.Limit {
			break
		}

		metadata, err := s.storage.GetMetadataFlat(hashID)
		if err != nil || metadata == nil {
			continue
		}

		matches := false

		// Property search
		if req.Property != "" && req.PropertyValue != "" {
			value, ok := metadata[req.Property]
			if ok && strings.Contains(strings.ToLower(value), strings.ToLower(req.PropertyValue)) {
				matches = true
			}
		}

		// General query search
		if req.Query != "" && req.Property == "" {
			queryLower := strings.ToLower(req.Query)

			// Check if query matches hashId
			if strings.Contains(strings.ToLower(hashID), queryLower) {
				matches = true
			}

			// Check search fields
			if !matches {
				for _, field := range searchFields {
					if value, ok := metadata[field]; ok {
						if strings.Contains(strings.ToLower(value), queryLower) {
							matches = true
							break
						}
					}
				}
			}
		}

		// If no query or property filter, return all
		if req.Query == "" && req.Property == "" {
			matches = true
		}

		if matches {
			results = append(results, MetadataSearchResult{
				HashID:   hashID,
				Metadata: metadata,
			})
		}
	}

	writeJSON(w, http.StatusOK, MetadataSearchResponse{
		Results: results,
		Count:   len(results),
		Total:   total,
	})
}

// handleBatchUpdate handles POST /api/metadata/batch
func (s *Server) handleBatchUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	var req MetadataBatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if len(req.Updates) == 0 {
		writeError(w, http.StatusBadRequest, "updates array is required")
		return
	}

	var results []MetadataBatchResult
	successCount := 0
	errorCount := 0

	for _, update := range req.Updates {
		if update.HashID == "" {
			results = append(results, MetadataBatchResult{
				HashID: "unknown",
				Status: "error",
				Error:  "Missing hash ID",
			})
			errorCount++
			continue
		}

		var updateErr error

		// Update complete metadata
		if update.Metadata != nil {
			// Exclude internal processing status
			delete(update.Metadata, "processingStatus")
			updateErr = s.storage.SetMetadataFlat(update.HashID, update.Metadata)
		}

		// Update individual properties
		if updateErr == nil && update.Properties != nil {
			for property, value := range update.Properties {
				if err := s.storage.SetProperty(update.HashID, property, value); err != nil {
					updateErr = err
					break
				}
			}
		}

		if updateErr != nil {
			results = append(results, MetadataBatchResult{
				HashID: update.HashID,
				Status: "error",
				Error:  updateErr.Error(),
			})
			errorCount++
		} else {
			results = append(results, MetadataBatchResult{
				HashID: update.HashID,
				Status: "ok",
			})
			successCount++
		}
	}

	writeJSON(w, http.StatusOK, MetadataBatchResponse{
		Status:  "completed",
		Total:   len(results),
		Success: successCount,
		Errors:  errorCount,
		Results: results,
	})
}

// handleClearMetadata handles POST /api/metadata/clear
func (s *Server) handleClearMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.storage.IsConnected() {
		writeError(w, http.StatusServiceUnavailable, "storage not connected")
		return
	}

	deletedCount, err := s.storage.ClearAllMetadata()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":       "ok",
		"message":      "Metadata cleared",
		"deletedCount": deletedCount,
	})
}
