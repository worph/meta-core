package api

// End-to-end exercise of the account gate against a live router + miniredis:
// generate -> write UDL rows -> stats -> challenge -> sign -> delete -> verify
// both the key and every row are gone. Deliberately drives the real HTTP
// handlers rather than the packages beneath them, because the bugs that matter
// here (a challenge accepted for the wrong action, a purge that runs after the
// key is gone) live in the wiring.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/mux"
	"github.com/metazla/meta-core/internal/config"
	"github.com/metazla/meta-core/internal/identity"
	"github.com/metazla/meta-core/internal/storage"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	mr := miniredis.RunT(t)
	stor := storage.NewClient("")
	if err := stor.Connect("redis://" + mr.Addr()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = stor.Close() })

	s := &Server{
		config:     &config.Config{MetaCorePath: t.TempDir()},
		storage:    stor,
		challenges: identity.NewChallengeStore(),
		router:     mux.NewRouter(),
	}
	s.setupIdentityTestRoutes()
	return s
}

// Only the routes under test, so this does not depend on every other subsystem
// booting.
func (s *Server) setupIdentityTestRoutes() {
	s.router.HandleFunc("/api/identity/accounts", s.handleIdentityAccounts).Methods("GET")
	s.router.HandleFunc("/api/identity/challenge", s.handleIdentityChallenge).Methods("POST")
	s.router.HandleFunc("/api/identity/generate", s.handleIdentityGenerate).Methods("POST")
	s.router.HandleFunc("/api/identity/reveal", s.handleIdentityReveal).Methods("POST")
	s.router.HandleFunc("/api/identity", s.handleIdentityDelete).Methods("DELETE")
	s.router.HandleFunc("/api/identity/sign", s.handleIdentitySign).Methods("POST")
	s.router.HandleFunc("/api/udl/users/stats", s.handleUDLUserStats).Methods("GET")
}

func do(t *testing.T, s *Server, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// signAs produces the proof a browser would, from the raw key.
func signAs(t *testing.T, privHex, challenge string) map[string]any {
	t.Helper()
	raw, err := hex.DecodeString(privHex)
	if err != nil {
		t.Fatalf("decode key: %v", err)
	}
	id, err := identity.Import(hex.EncodeToString(raw))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	sig, err := identity.Sign(id, []byte(challenge))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return map[string]any{
		"challenge": challenge,
		"signature": base64.StdEncoding.EncodeToString(sig),
	}
}

func TestE2E_DeleteRequiresProofAndPurgesEverything(t *testing.T) {
	s := newTestServer(t)

	rec, gen := do(t, s, "POST", "/api/identity/generate", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body)
	}
	uid := gen["uid"].(string)
	priv := gen["privateKeyHex"].(string)

	// Some data to lose.
	for _, cell := range [][2]string{{"cidA", "like"}, {"cidA", "rating"}, {"cidB", "seek"}} {
		if ok, err := s.storage.UDLUpsertIfNewer(uid, cell[0], cell[1], 1, 100, "rec", "", false, false); err != nil || !ok {
			t.Fatalf("seed: %v", err)
		}
	}

	// The count the UI shows in the confirmation.
	rec, stats := do(t, s, "GET", "/api/udl/users/stats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body)
	}
	users := stats["users"].(map[string]any)
	mine := users[uid].(map[string]any)
	if mine["records"].(float64) != 3 || mine["cids"].(float64) != 2 {
		t.Fatalf("stats = %v, want 3 records / 2 cids", mine)
	}

	// Unsigned delete must be refused, and must not have touched anything.
	rec, body := do(t, s, "DELETE", "/api/identity", map[string]any{"uid": uid, "confirm": true})
	if rec.Code != http.StatusUnauthorized || body["error"] != "signature_required" {
		t.Fatalf("unsigned delete = %d %v, want 401 signature_required", rec.Code, body)
	}
	if !identity.ExistsByUID(s.config.IdentityAccountsDir(), uid) {
		t.Fatal("a refused delete removed the key anyway")
	}

	// meta-core must refuse to sign the caller's authorisation for them —
	// without this the gate is decoration on a node holding every key.
	ch, _, err := s.challenges.Issue(uid, identity.ActionDelete)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	rec, body = do(t, s, "POST", "/api/identity/sign", map[string]any{
		"uid":      uid,
		"bytesB64": base64.StdEncoding.EncodeToString([]byte(ch)),
	})
	if rec.Code != http.StatusForbidden || body["error"] != "reserved_payload" {
		t.Fatalf("sign of a challenge = %d %v, want 403 reserved_payload", rec.Code, body)
	}

	// A challenge minted for reveal must not authorise a delete.
	rec, chBody := do(t, s, "POST", "/api/identity/challenge", map[string]any{"uid": uid, "action": "reveal"})
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body)
	}
	proof := signAs(t, priv, chBody["challenge"].(string))
	req := map[string]any{"uid": uid, "confirm": true}
	for k, v := range proof {
		req[k] = v
	}
	rec, body = do(t, s, "DELETE", "/api/identity", req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reveal challenge used for delete = %d %v, want 401", rec.Code, body)
	}

	// The real thing.
	rec, chBody = do(t, s, "POST", "/api/identity/challenge", map[string]any{"uid": uid, "action": "delete"})
	if rec.Code != http.StatusOK {
		t.Fatalf("challenge: %d %s", rec.Code, rec.Body)
	}
	proof = signAs(t, priv, chBody["challenge"].(string))
	req = map[string]any{"uid": uid, "confirm": true}
	for k, v := range proof {
		req[k] = v
	}
	rec, body = do(t, s, "DELETE", "/api/identity", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("signed delete = %d %s", rec.Code, rec.Body)
	}
	purged := body["purged"].(map[string]any)
	if purged["records"].(float64) != 3 || purged["cids"].(float64) != 2 {
		t.Fatalf("purged = %v, want 3 records / 2 cids", purged)
	}

	// Key gone.
	if identity.ExistsByUID(s.config.IdentityAccountsDir(), uid) {
		t.Fatal("the account file survived the delete")
	}
	// Data gone — the orphaning this whole change exists to prevent.
	after, err := s.storage.UDLAllUserStats()
	if err != nil {
		t.Fatalf("stats after: %v", err)
	}
	if got, present := after[uid]; present {
		t.Fatalf("deleted account still holds %+v", got)
	}
	for _, cid := range []string{"cidA", "cidB"} {
		uids, err := s.storage.UDLCidUsers(cid)
		if err != nil {
			t.Fatalf("cid users: %v", err)
		}
		if len(uids) != 0 {
			t.Fatalf("%s still names %v after the purge", cid, uids)
		}
	}

	// The challenge is single-use, so replaying the same proof does nothing.
	rec, _ = do(t, s, "DELETE", "/api/identity", req)
	if rec.Code == http.StatusOK {
		t.Fatal("a replayed proof must not succeed")
	}
}

func TestE2E_RevealRequiresProof(t *testing.T) {
	s := newTestServer(t)
	_, gen := do(t, s, "POST", "/api/identity/generate", nil)
	uid := gen["uid"].(string)
	priv := gen["privateKeyHex"].(string)

	rec, body := do(t, s, "POST", "/api/identity/reveal", map[string]any{"uid": uid, "confirm": true})
	if rec.Code != http.StatusUnauthorized || body["error"] != "signature_required" {
		t.Fatalf("unsigned reveal = %d %v, want 401 signature_required", rec.Code, body)
	}

	_, chBody := do(t, s, "POST", "/api/identity/challenge", map[string]any{"uid": uid, "action": "reveal"})
	proof := signAs(t, priv, chBody["challenge"].(string))
	req := map[string]any{"uid": uid, "confirm": true}
	for k, v := range proof {
		req[k] = v
	}
	rec, body = do(t, s, "POST", "/api/identity/reveal", req)
	if rec.Code != http.StatusOK {
		t.Fatalf("signed reveal = %d %s", rec.Code, rec.Body)
	}
	if body["privateKeyHex"] != priv {
		t.Fatal("reveal returned a different key than generate did")
	}
}

// Another account's key must not authorise this one, even with a challenge
// minted for the right uid and action.
func TestE2E_AnotherAccountsKeyIsRefused(t *testing.T) {
	s := newTestServer(t)
	_, alice := do(t, s, "POST", "/api/identity/generate", nil)
	_, mallory := do(t, s, "POST", "/api/identity/generate", nil)

	uid := alice["uid"].(string)
	_, chBody := do(t, s, "POST", "/api/identity/challenge", map[string]any{"uid": uid, "action": "delete"})
	proof := signAs(t, mallory["privateKeyHex"].(string), chBody["challenge"].(string))
	req := map[string]any{"uid": uid, "confirm": true}
	for k, v := range proof {
		req[k] = v
	}
	rec, body := do(t, s, "DELETE", "/api/identity", req)
	if rec.Code != http.StatusUnauthorized || body["error"] != "bad_signature" {
		t.Fatalf("mallory deleting alice = %d %v, want 401 bad_signature", rec.Code, body)
	}
	if !identity.ExistsByUID(s.config.IdentityAccountsDir(), uid) {
		t.Fatal("alice's account was deleted by mallory's signature")
	}
}
