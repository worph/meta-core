package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/metazla/meta-core/internal/identity"
	"github.com/metazla/meta-core/internal/storage"
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
	// Created distinguishes "this key was new here" (201) from "this key was
	// already in the keystore" (200). Import is the sign-in primitive for
	// clients: presenting the secret key is the proof of ownership, so a
	// re-import must succeed rather than conflict. Clients use the flag only
	// to word the confirmation ("new profile" vs "welcome back").
	Created bool `json:"created"`
}

// identityChallengeRequest asks for a one-shot challenge to sign.
type identityChallengeRequest struct {
	UID    string `json:"uid"`
	Action string `json:"action"`
}

type identityChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt int64  `json:"expiresAt"`
}

// identityProof is the proof-of-possession carried by reveal and delete: the
// challenge text as issued, and a signature over it by the account's key.
//
// The signature is produced by whoever holds the key — the browser, locally.
// It is NOT obtainable from POST /api/identity/sign, which refuses any payload
// in the challenge domain; that refusal is what makes this proof mean
// something on a node that stores every private key in plaintext.
type identityProof struct {
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
}

// identityRevealRequest asks for an existing account's private key back.
//
// Gated on proof-of-possession, which makes it a "does this node hold the same
// key I do" check rather than the recovery path it once was. That is the
// deliberate consequence of the rule this file enforces: the private key, and
// nothing else, is authority over the account. An operator with root on the box
// can still read the file; an operator with only the API cannot.
type identityRevealRequest struct {
	Confirm bool   `json:"confirm"`
	UID     string `json:"uid"`
	identityProof
}

type identityRevealResponse struct {
	UID           string `json:"uid"`
	Curve         string `json:"curve"`
	PrivateKeyHex string `json:"privateKeyHex"`
}

type identityDeleteRequest struct {
	Confirm bool   `json:"confirm"`
	UID     string `json:"uid"`
	identityProof
}

// identityDeleteResponse reports what the delete actually removed. The purge
// counts are the point: "account removed" alone leaves the caller unable to
// tell a clean removal from one that silently stranded a decade of ratings.
type identityDeleteResponse struct {
	UID    string               `json:"uid"`
	Purged storage.UDLUserStats `json:"purged"`
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

// identitySignBatchRequest signs many payloads under one account in one call.
//
// Exists because a User Data Layer write signs exactly one record, and the bulk
// verbs above it ("mark this whole series seen") produce one record per episode:
// a 300-episode show is 300 sequential sign round trips through
// meta-watch -> meta-search -> here, which dominates the operation. The private
// key still never leaves this process; only the transport is batched.
type identitySignBatchRequest struct {
	// PayloadsB64 are the canonical byte strings to sign, in order. The
	// response's signatures line up index-for-index.
	PayloadsB64 []string `json:"payloadsB64"`
	// UID selects which account signs — same optional single-identity
	// back-compat as identitySignRequest.
	UID string `json:"uid,omitempty"`
}

type identitySignBatchResponse struct {
	SigsB64 []string `json:"sigsB64"`
}

// maxSignBatch bounds one batch. A signature is cheap but not free, and the
// request holds the account lock for its duration; a caller with more than this
// should page. Sized well above the longest real series (Supernatural, 327).
const maxSignBatch = 1000

// reservedPayloadMessage explains the one refusal the signing endpoints make.
//
// This is the load-bearing half of proof-of-possession on a node that holds
// every private key: without it, a caller wanting to delete someone else's
// account just asks meta-core to sign the challenge for them, and the gate on
// reveal/delete becomes a formality. See identity.ChallengeDomain.
const reservedPayloadMessage = "payload is in the reserved account-authorisation domain (" +
	identity.ChallengeDomain + "…); meta-core will not sign one. " +
	"Signing a challenge is what proves you hold the key, so it must be done where the key is."

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
	// Idempotent: importing a key that is already in the keystore is how a
	// second device signs in to an existing account. Answer 200 with the
	// stored account (preserving its original createdAt) instead of 409 —
	// the caller proved ownership by presenting the key.
	if existing, err := identity.LoadByUID(dir, id.UID); err == nil && existing != nil {
		writeJSON(w, http.StatusOK, identityImportResponse{
			UID:       existing.UID,
			Curve:     existing.Curve,
			CreatedAt: existing.CreatedAt,
			Created:   false,
		})
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
		Created:   true,
	})
}

// handleIdentityChallenge issues the one-shot text a client signs to prove it
// holds an account's private key.
//
// Unauthenticated by design: a challenge is useless without the key, and
// GET /api/identity/accounts already publishes every uid, so refusing to mint
// one for an unknown caller would protect nothing. Requiring the account to
// exist is for a clear error, not secrecy.
func (s *Server) handleIdentityChallenge(w http.ResponseWriter, r *http.Request) {
	var body identityChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	uid := strings.TrimSpace(body.UID)
	if uid == "" {
		writeErrorSlug(w, http.StatusBadRequest, "uid_required", "uid is required", false)
		return
	}
	action := identity.Action(strings.TrimSpace(body.Action))
	if !identity.ValidAction(action) {
		writeErrorSlug(w, http.StatusBadRequest, "bad_action",
			`action must be "reveal" or "delete"`, false)
		return
	}
	if !identity.ExistsByUID(s.config.IdentityAccountsDir(), uid) {
		writeErrorSlug(w, http.StatusNotFound, "no_identity", "no identity for the given uid", false)
		return
	}
	text, expires, err := s.challenges.Issue(uid, action)
	if err != nil {
		if errors.Is(err, identity.ErrChallengeFull) {
			writeErrorSlug(w, http.StatusServiceUnavailable, "challenges_full", err.Error(), true)
			return
		}
		writeErrorSlug(w, http.StatusBadRequest, "bad_uid", err.Error(), false)
		return
	}
	writeJSON(w, http.StatusOK, identityChallengeResponse{
		Challenge: text,
		ExpiresAt: expires.Unix(),
	})
}

// requireProof resolves the target account and verifies the caller's
// proof-of-possession for `action`. status == 0 means the proof held.
//
// The uid is mandatory here, unlike elsewhere in this file: the old "sole
// account" fallback would have this pick a target the signature does not name,
// and a proof that authorises an unnamed account authorises the wrong one as
// soon as a second profile exists.
func (s *Server) requireProof(uid string, proof identityProof, action identity.Action) (
	id *identity.Identity, status int, slug, msg string,
) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return nil, http.StatusBadRequest, "uid_required",
			"uid is required: the signature authorises one specific account"
	}
	if proof.Challenge == "" || proof.Signature == "" {
		return nil, http.StatusUnauthorized, "signature_required",
			"this operation requires proof you hold the account's private key: " +
				"POST /api/identity/challenge, sign the returned text, and resend it as {challenge, signature}"
	}
	sig, err := base64.StdEncoding.DecodeString(proof.Signature)
	if err != nil {
		return nil, http.StatusBadRequest, "bad_signature", "signature is not valid base64"
	}
	// Resolve before redeeming so a request naming a missing account does not
	// burn a challenge it could not have used anyway.
	id, status, slug, msg = s.resolveAccount(uid)
	if status != 0 {
		return nil, status, slug, msg
	}
	// Redeem first, verify second: the challenge is single-use either way, so a
	// wrong signature costs the attacker a fresh round trip per guess rather
	// than letting them grind one challenge offline against the same nonce.
	if err := s.challenges.Redeem(proof.Challenge, uid, action); err != nil {
		return nil, http.StatusUnauthorized, "bad_challenge", err.Error()
	}
	if err := identity.Verify(uid, []byte(proof.Challenge), sig); err != nil {
		return nil, http.StatusUnauthorized, "bad_signature", err.Error()
	}
	return id, 0, "", ""
}

// handleIdentityReveal returns an existing account's private key to a caller
// that proves it already holds that key.
//
// Deliberately POST (never a bare GET) so it cannot be triggered by a link or
// an image tag. The proof is what actually guards it: before this gate existed,
// anything that could reach the port could read every private key on the node.
func (s *Server) handleIdentityReveal(w http.ResponseWriter, r *http.Request) {
	var body identityRevealRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	uid := strings.TrimSpace(body.UID)
	if uid == "" {
		uid = strings.TrimSpace(r.URL.Query().Get("uid"))
	}
	id, status, slug, msg := s.requireProof(uid, body.identityProof, identity.ActionReveal)
	if status != 0 {
		writeErrorSlug(w, status, slug, msg, false)
		return
	}
	writeJSON(w, http.StatusOK, identityRevealResponse{
		UID:           id.UID,
		Curve:         id.Curve,
		PrivateKeyHex: id.PrivateKeyHex,
	})
}

// handleIdentityDelete removes one account and everything it wrote.
//
// Two things happen, in this order and no other:
//
//  1. the account's User Data Layer rows are purged (cells plus every index
//     that names the uid, including the cid-scoped ones shared with other
//     accounts);
//  2. the keypair file is removed.
//
// Purge-then-delete so a failure never strands data: if the purge errors the
// key is still on disk, the account still exists, and the caller can retry.
// The reverse order would leave rows nobody can ever attribute, reach or
// rewrite — the orphaning this endpoint exists to prevent.
//
// What deletion still does NOT do, and cannot: records already signed under
// this uid and replicated to other peers keep verifying there. Removing a key
// is not a recall.
func (s *Server) handleIdentityDelete(w http.ResponseWriter, r *http.Request) {
	var body identityDeleteRequest
	// Tolerate an empty body so the uid may ride in the query — meta-watch's
	// proxy puts it there. The proof itself must be in the body: a signature in
	// a URL ends up in every access log between here and the browser.
	_ = json.NewDecoder(r.Body).Decode(&body)
	uid := strings.TrimSpace(body.UID)
	if uid == "" {
		uid = strings.TrimSpace(r.URL.Query().Get("uid"))
	}
	id, status, slug, msg := s.requireProof(uid, body.identityProof, identity.ActionDelete)
	if status != 0 {
		writeErrorSlug(w, status, slug, msg, false)
		return
	}

	// A purge needs storage. Refusing here — rather than deleting the key and
	// reporting the purge as "0 records" — is the difference between a retry
	// and permanent orphaned data: once the key is gone the account cannot be
	// named for a second attempt through this API.
	if !s.storage.IsConnected() {
		writeErrorSlug(w, http.StatusServiceUnavailable, "storage_unavailable",
			"storage is not connected; refusing to delete the key while its data cannot be purged", true)
		return
	}
	purged, err := s.storage.UDLPurgeUser(id.UID)
	if err != nil {
		writeErrorSlug(w, http.StatusInternalServerError, "purge_failed",
			"could not purge this account's data; the account was NOT deleted: "+err.Error(), true)
		return
	}

	if err := identity.DeleteByUID(s.config.IdentityAccountsDir(), id.UID); err != nil {
		writeErrorSlug(w, http.StatusInternalServerError, "delete_failed", err.Error(), false)
		return
	}
	log.Printf("[API] Deleted account %s and purged %d User Data Layer records across %d cids",
		id.UID, purged.Records, purged.Cids)
	writeJSON(w, http.StatusOK, identityDeleteResponse{UID: id.UID, Purged: purged})
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
	if identity.IsChallengePayload(raw) {
		writeErrorSlug(w, http.StatusForbidden, "reserved_payload", reservedPayloadMessage, false)
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

// handleIdentitySignBatch signs every payload in the request under one account.
//
// All-or-nothing: a single bad base64 or signing failure fails the whole
// request rather than returning a short array. The caller pairs signatures with
// records by index, so a partial response would silently misalign them — and a
// misaligned signature verifies against the wrong record, which is worse than
// an error.
func (s *Server) handleIdentitySignBatch(w http.ResponseWriter, r *http.Request) {
	var body identitySignBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	uid := body.UID
	if strings.TrimSpace(uid) == "" {
		uid = r.URL.Query().Get("uid")
	}
	if len(body.PayloadsB64) == 0 {
		writeError(w, http.StatusBadRequest, "payloadsB64 is required")
		return
	}
	if len(body.PayloadsB64) > maxSignBatch {
		writeError(w, http.StatusBadRequest, "too many payloads in one batch")
		return
	}
	id, status, slug, msg := s.resolveAccount(uid)
	if status != 0 {
		writeErrorSlug(w, status, slug, msg, false)
		return
	}
	sigs := make([]string, 0, len(body.PayloadsB64))
	for i, p := range body.PayloadsB64 {
		raw, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			writeError(w, http.StatusBadRequest, "payloadsB64["+strconv.Itoa(i)+"] is not valid base64")
			return
		}
		if identity.IsChallengePayload(raw) {
			writeErrorSlug(w, http.StatusForbidden, "reserved_payload",
				"payloadsB64["+strconv.Itoa(i)+"]: "+reservedPayloadMessage, false)
			return
		}
		sig, err := identity.Sign(id, raw)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sigs = append(sigs, base64.StdEncoding.EncodeToString(sig))
	}
	writeJSON(w, http.StatusOK, identitySignBatchResponse{SigsB64: sigs})
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
