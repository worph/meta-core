package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/metazla/meta-core/internal/identity"
)

// identityStatusResponse is the shape of GET /api/identity. The private key
// is intentionally absent — it is only ever returned once, by
// POST /api/identity/generate, immediately after creation.
type identityStatusResponse struct {
	HasIdentity bool   `json:"hasIdentity"`
	UID         string `json:"uid,omitempty"`
	Curve       string `json:"curve,omitempty"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
}

type identityGenerateResponse struct {
	UID           string `json:"uid"`
	Curve         string `json:"curve"`
	CreatedAt     int64  `json:"createdAt"`
	PrivateKeyHex string `json:"privateKeyHex"`
}

type identityImportRequest struct {
	PrivateKeyHex string `json:"privateKeyHex"`
}

type identityImportResponse struct {
	UID       string `json:"uid"`
	Curve     string `json:"curve"`
	CreatedAt int64  `json:"createdAt"`
}

type identityDeleteRequest struct {
	Confirm bool `json:"confirm"`
}

type identitySignRequest struct {
	BytesB64 string `json:"bytesB64"`
}

type identitySignResponse struct {
	SigB64 string `json:"sigB64"`
}

type identityAEADKeyResponse struct {
	AEADKeyB64 string `json:"aeadKeyB64"`
}

func (s *Server) handleIdentityGet(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Load(s.config.IdentityFilePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id == nil {
		writeJSON(w, http.StatusOK, identityStatusResponse{HasIdentity: false})
		return
	}
	writeJSON(w, http.StatusOK, identityStatusResponse{
		HasIdentity: true,
		UID:         id.UID,
		Curve:       id.Curve,
		CreatedAt:   id.CreatedAt,
	})
}

func (s *Server) handleIdentityGenerate(w http.ResponseWriter, r *http.Request) {
	if identity.Exists(s.config.IdentityFilePath()) {
		writeErrorSlug(w, http.StatusConflict, "identity_exists",
			"identity already exists; delete it first to generate a new one", false)
		return
	}
	id, err := identity.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := identity.Save(s.config.IdentityFilePath(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, identityGenerateResponse{
		UID:           id.UID,
		Curve:         id.Curve,
		CreatedAt:     id.CreatedAt,
		PrivateKeyHex: id.PrivateKeyHex,
	})
}

func (s *Server) handleIdentityImport(w http.ResponseWriter, r *http.Request) {
	if identity.Exists(s.config.IdentityFilePath()) {
		writeErrorSlug(w, http.StatusConflict, "identity_exists",
			"identity already exists; delete it first to import a new one", false)
		return
	}
	var body identityImportRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.PrivateKeyHex) == "" {
		writeError(w, http.StatusBadRequest, "privateKeyHex is required")
		return
	}
	id, err := identity.Import(strings.TrimSpace(body.PrivateKeyHex))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := identity.Save(s.config.IdentityFilePath(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, identityImportResponse{
		UID:       id.UID,
		Curve:     id.Curve,
		CreatedAt: id.CreatedAt,
	})
}

func (s *Server) handleIdentityDelete(w http.ResponseWriter, r *http.Request) {
	var body identityDeleteRequest
	// Tolerate empty body — confirm flag in a query param is also acceptable
	// for shells where sending a JSON body with DELETE is awkward.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !body.Confirm && r.URL.Query().Get("confirm") != "true" {
		writeErrorSlug(w, http.StatusBadRequest, "confirm_required",
			"identity deletion requires {\"confirm\": true} or ?confirm=true", false)
		return
	}
	if err := identity.Delete(s.config.IdentityFilePath()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIdentitySign(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Load(s.config.IdentityFilePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id == nil {
		writeErrorSlug(w, http.StatusNotFound, "no_identity",
			"no identity is configured", false)
		return
	}
	var body identitySignRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.BytesB64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bytesB64 is not valid base64")
		return
	}
	sig, err := identity.Sign(id, raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, identitySignResponse{
		SigB64: base64.StdEncoding.EncodeToString(sig),
	})
}

func (s *Server) handleIdentityAEADKey(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Load(s.config.IdentityFilePath())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if id == nil {
		writeErrorSlug(w, http.StatusNotFound, "no_identity",
			"no identity is configured", false)
		return
	}
	key, err := identity.DeriveAEADKey(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, identityAEADKeyResponse{
		AEADKeyB64: base64.StdEncoding.EncodeToString(key),
	})
}
