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
	// AccountCount is the total number of accounts in the keystore. Lets
	// multi-account clients (meta-watch) tell "single identity" from "pick a
	// profile" without a second call. Older single-identity clients ignore it.
	AccountCount int `json:"accountCount,omitempty"`
}

// identityAccountInfo is one entry in GET /api/identity/accounts. Never carries
// the private key — that is only ever returned once, by generate.
type identityAccountInfo struct {
	UID       string `json:"uid"`
	Curve     string `json:"curve"`
	CreatedAt int64  `json:"createdAt"`
}

type identityAccountsResponse struct {
	Accounts []identityAccountInfo `json:"accounts"`
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
	Confirm bool   `json:"confirm"`
	UID     string `json:"uid"`
}

type identitySignRequest struct {
	BytesB64 string `json:"bytesB64"`
	// UID selects which account signs. Optional — when omitted and exactly one
	// account exists, that account is used (single-identity back-compat).
	UID string `json:"uid,omitempty"`
}

type identitySignResponse struct {
	SigB64 string `json:"sigB64"`
}

type identityAEADKeyResponse struct {
	AEADKeyB64 string `json:"aeadKeyB64"`
}

// resolveAccount picks the account an operation targets. A non-empty uid must
// match an existing account. An empty uid falls back to the sole account
// (single-identity back-compat); with zero or many accounts and no uid it
// returns a non-zero status for the caller to surface. status == 0 means ok.
func (s *Server) resolveAccount(uid string) (id *identity.Identity, status int, slug, msg string) {
	dir := s.config.IdentityAccountsDir()
	uid = strings.TrimSpace(uid)
	if uid != "" {
		got, err := identity.LoadByUID(dir, uid)
		if err != nil {
			return nil, http.StatusBadRequest, "bad_uid", err.Error()
		}
		if got == nil {
			return nil, http.StatusNotFound, "no_identity", "no identity for the given uid"
		}
		return got, 0, "", ""
	}
	list, err := identity.List(dir)
	if err != nil {
		return nil, http.StatusInternalServerError, "list_failed", err.Error()
	}
	switch len(list) {
	case 0:
		return nil, http.StatusNotFound, "no_identity", "no identity is configured"
	case 1:
		return list[0], 0, "", ""
	default:
		return nil, http.StatusBadRequest, "uid_required", "multiple accounts exist; specify uid"
	}
}

func (s *Server) handleIdentityGet(w http.ResponseWriter, r *http.Request) {
	list, err := identity.List(s.config.IdentityAccountsDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(list) == 0 {
		writeJSON(w, http.StatusOK, identityStatusResponse{HasIdentity: false})
		return
	}
	// Back-compat: report the oldest account as "the" identity. Multi-account
	// clients enumerate via GET /api/identity/accounts instead.
	def := list[0]
	writeJSON(w, http.StatusOK, identityStatusResponse{
		HasIdentity:  true,
		UID:          def.UID,
		Curve:        def.Curve,
		CreatedAt:    def.CreatedAt,
		AccountCount: len(list),
	})
}

func (s *Server) handleIdentityAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := identity.List(s.config.IdentityAccountsDir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]identityAccountInfo, 0, len(list))
	for _, id := range list {
		out = append(out, identityAccountInfo{UID: id.UID, Curve: id.Curve, CreatedAt: id.CreatedAt})
	}
	writeJSON(w, http.StatusOK, identityAccountsResponse{Accounts: out})
}

func (s *Server) handleIdentityGenerate(w http.ResponseWriter, r *http.Request) {
	id, err := identity.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := identity.SaveAccount(s.config.IdentityAccountsDir(), id); err != nil {
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
	dir := s.config.IdentityAccountsDir()
	if identity.ExistsByUID(dir, id.UID) {
		writeErrorSlug(w, http.StatusConflict, "identity_exists",
			"an account with this key already exists", false)
		return
	}
	if err := identity.SaveAccount(dir, id); err != nil {
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
	dir := s.config.IdentityAccountsDir()
	uid := strings.TrimSpace(body.UID)
	if uid == "" {
		uid = strings.TrimSpace(r.URL.Query().Get("uid"))
	}
	if uid == "" {
		// Back-compat: with no uid, delete the sole account. Ambiguous (and so
		// refused) once more than one account exists.
		list, err := identity.List(dir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		switch len(list) {
		case 0:
			w.WriteHeader(http.StatusNoContent) // idempotent
			return
		case 1:
			uid = list[0].UID
		default:
			writeErrorSlug(w, http.StatusBadRequest, "uid_required",
				"multiple accounts exist; specify uid to delete", false)
			return
		}
	}
	if err := identity.DeleteByUID(dir, uid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleIdentitySign(w http.ResponseWriter, r *http.Request) {
	var body identitySignRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	uid := body.UID
	if strings.TrimSpace(uid) == "" {
		uid = r.URL.Query().Get("uid")
	}
	id, status, slug, msg := s.resolveAccount(uid)
	if status != 0 {
		writeErrorSlug(w, status, slug, msg, false)
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
	id, status, slug, msg := s.resolveAccount(r.URL.Query().Get("uid"))
	if status != 0 {
		writeErrorSlug(w, status, slug, msg, false)
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
