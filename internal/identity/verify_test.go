package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// The whole gate rests on a uid being the public key: verification consults no
// stored state, so a signature is checked against the account the caller names
// rather than against whatever key meta-core happens to hold.
func TestVerify_AcceptsDERFromOurOwnSigner(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	payload := []byte(ChallengeDomain + "delete:" + id.UID + ":abcdef")

	sig, err := Sign(id, payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(id.UID, payload, sig); err != nil {
		t.Fatalf("DER signature from Sign must verify: %v", err)
	}
}

// The browser is the signer that matters here, and noble-secp256k1 v3 emits
// 64-byte compact r‖s — it dropped DER entirely. A verifier that took only DER
// would reject every real proof while passing this package's own tests.
func TestVerify_AcceptsCompactAsTheBrowserEmitsIt(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	payload := []byte("some challenge text")

	raw, _ := hex.DecodeString(id.PrivateKeyHex)
	priv := secp256k1.PrivKeyFromBytes(raw)
	digest := sha256.Sum256(payload)
	der := ecdsa.Sign(priv, digest[:])

	// Re-encode the same signature compactly, the way the browser would.
	parsed, err := ecdsa.ParseDERSignature(der.Serialize())
	if err != nil {
		t.Fatalf("parse own DER: %v", err)
	}
	r := parsed.R()
	sc := parsed.S()
	compact := make([]byte, 0, 64)
	rb := r.Bytes()
	sb := sc.Bytes()
	compact = append(compact, rb[:]...)
	compact = append(compact, sb[:]...)

	if err := Verify(id.UID, payload, compact); err != nil {
		t.Fatalf("compact signature must verify: %v", err)
	}
}

func TestVerify_RejectsAnotherAccountsSignature(t *testing.T) {
	alice, _ := Generate()
	mallory, _ := Generate()
	payload := []byte("challenge for alice")

	sig, err := Sign(mallory, payload)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := Verify(alice.UID, payload, sig); err == nil {
		t.Fatal("a signature by mallory must not verify as alice")
	}
}

func TestVerify_RejectsTamperedPayload(t *testing.T) {
	id, _ := Generate()
	sig, _ := Sign(id, []byte("delete account A"))
	if err := Verify(id.UID, []byte("delete account B"), sig); err == nil {
		t.Fatal("a signature must not verify over different bytes")
	}
}

func TestVerify_RejectsGarbageSignatureEncodings(t *testing.T) {
	id, _ := Generate()
	for name, sig := range map[string][]byte{
		"empty":            {},
		"short":            {1, 2, 3},
		"zero compact":     make([]byte, 64),
		"not der":          []byte("this is not a signature at all......."),
		"63 bytes":         make([]byte, 63),
		"65 bytes non-der": make([]byte, 65),
	} {
		if err := Verify(id.UID, []byte("x"), sig); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestPubkeyFromUID_RejectsMalformedUIDs(t *testing.T) {
	id, _ := Generate()
	for name, uid := range map[string]string{
		"empty":          "",
		"no z prefix":    id.UID[1:],
		"truncated":      id.UID[:20],
		"path traversal": "../../etc/passwd",
		"with slash":     "z/" + id.UID,
		"not on curve":   "z" + strings.Repeat("1", 45),
	} {
		if _, err := PubkeyFromUID(uid); err == nil {
			t.Errorf("%s (%q): expected rejection", name, uid)
		}
	}
	if _, err := PubkeyFromUID(id.UID); err != nil {
		t.Fatalf("a real uid must parse: %v", err)
	}
}

// The refusal that makes proof-of-possession mean anything on a node holding
// every private key. If this ever returns false for a challenge, an attacker
// asks /api/identity/sign to authorise their own delete.
func TestIsChallengePayload_CatchesAnythingInTheDomain(t *testing.T) {
	yes := [][]byte{
		[]byte(ChallengeDomain),
		[]byte(ChallengeDomain + "delete:zAlice:beef"),
		[]byte(ChallengeDomain + "some-future-action:whatever"),
		[]byte("metamesh-identity-v1:"), // literal, in case the const drifts
	}
	for _, b := range yes {
		if !IsChallengePayload(b) {
			t.Errorf("must refuse to sign %q", b)
		}
	}
	no := [][]byte{
		{},
		[]byte("metamesh-identity-v2:delete:zAlice"), // different domain, not ours to refuse
		[]byte("a record payload"),
		[]byte(" " + ChallengeDomain), // not a prefix
	}
	for _, b := range no {
		if IsChallengePayload(b) {
			t.Errorf("must still sign %q", b)
		}
	}
}

func TestChallengeMatches_TiesTheTextToUIDAndAction(t *testing.T) {
	text, err := NewChallengeText("zAlice", ActionDelete)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !ChallengeMatches(text, "zAlice", ActionDelete) {
		t.Fatal("its own uid+action must match")
	}
	if ChallengeMatches(text, "zBob", ActionDelete) {
		t.Fatal("another uid must not match")
	}
	if ChallengeMatches(text, "zAlice", ActionReveal) {
		t.Fatal("another action must not match")
	}
	// A prefix-only uid must not slip through: "zAlic" is a prefix of "zAlice",
	// and a naive HasPrefix on the uid alone would accept it.
	if ChallengeMatches(text, "zAlic", ActionDelete) {
		t.Fatal("a uid that is a prefix of the real one must not match")
	}
}

func TestNewChallengeText_IsUnpredictable(t *testing.T) {
	seen := make(map[string]struct{}, 64)
	for i := 0; i < 64; i++ {
		text, err := NewChallengeText("zAlice", ActionDelete)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if _, dup := seen[text]; dup {
			t.Fatal("challenge repeated — the nonce is not random")
		}
		seen[text] = struct{}{}
	}
}

// --- challenge store --------------------------------------------------------

func TestChallengeStore_RedeemIsSingleUse(t *testing.T) {
	s := NewChallengeStore()
	text, _, err := s.Issue("zAlice", ActionDelete)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := s.Redeem(text, "zAlice", ActionDelete); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := s.Redeem(text, "zAlice", ActionDelete); err == nil {
		t.Fatal("a challenge must not be redeemable twice — that is replay")
	}
}

// A challenge minted to *look at* a key must not delete the account, and one
// minted for Alice must not act on Bob. Both are inside the signed text, so a
// store that ignored them would authorise the wrong operation with a perfectly
// valid signature.
func TestChallengeStore_RedeemRefusesWrongUIDOrAction(t *testing.T) {
	s := NewChallengeStore()

	text, _, _ := s.Issue("zAlice", ActionReveal)
	if err := s.Redeem(text, "zAlice", ActionDelete); err == nil {
		t.Fatal("a reveal challenge must not authorise a delete")
	}

	text2, _, _ := s.Issue("zAlice", ActionDelete)
	if err := s.Redeem(text2, "zBob", ActionDelete); err == nil {
		t.Fatal("alice's challenge must not authorise deleting bob")
	}
}

// A failed redemption still consumes the challenge, so a wrong guess costs a
// round trip rather than leaving the nonce up for another attempt.
func TestChallengeStore_FailedRedeemStillBurnsIt(t *testing.T) {
	s := NewChallengeStore()
	text, _, _ := s.Issue("zAlice", ActionDelete)
	_ = s.Redeem(text, "zBob", ActionDelete)
	if err := s.Redeem(text, "zAlice", ActionDelete); err == nil {
		t.Fatal("a burnt challenge must stay burnt for its rightful owner too")
	}
}

func TestChallengeStore_Expires(t *testing.T) {
	s := NewChallengeStore()
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }

	text, expires, _ := s.Issue("zAlice", ActionDelete)
	if !expires.Equal(now.Add(ChallengeTTL)) {
		t.Fatalf("expiry %v, want %v", expires, now.Add(ChallengeTTL))
	}
	now = now.Add(ChallengeTTL + time.Second)
	if err := s.Redeem(text, "zAlice", ActionDelete); err == nil {
		t.Fatal("an expired challenge must not redeem")
	}
}

// Issuing is unauthenticated, so the map is attacker-growable. The cap is what
// stops that being a memory-exhaustion lever; expiry sweeping is what stops the
// cap being reached by honest traffic.
func TestChallengeStore_CapsInFlightAndSweepsExpired(t *testing.T) {
	s := NewChallengeStore()
	now := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return now }

	for i := 0; i < maxChallenges; i++ {
		if _, _, err := s.Issue("zAlice", ActionDelete); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	if _, _, err := s.Issue("zAlice", ActionDelete); err == nil {
		t.Fatal("expected the store to refuse once full")
	}

	// Once they age out, the next issue sweeps and succeeds again.
	now = now.Add(ChallengeTTL + time.Second)
	if _, _, err := s.Issue("zAlice", ActionDelete); err != nil {
		t.Fatalf("after expiry the store must accept again: %v", err)
	}
}

func TestChallengeStore_RefusesInvalidInputs(t *testing.T) {
	s := NewChallengeStore()
	if _, _, err := s.Issue("../etc", ActionDelete); err == nil {
		t.Fatal("a uid that is not uid-shaped must be refused")
	}
	if _, _, err := s.Issue("zAlice", Action("sudo")); err == nil {
		t.Fatal("an unknown action must be refused")
	}
}
