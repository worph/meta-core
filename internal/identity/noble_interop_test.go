package identity

import (
	"encoding/base64"
	"testing"
)

// A real signature produced by @noble/secp256k1 v3 — the library the dashboard
// and meta-watch both sign with — pinned as a fixture and verified here.
//
// This is the one seam neither side's own tests can cover: Go signs DER, the
// browser signs 64-byte compact, and every unit test above verifies signatures
// this package produced itself. If the encodings ever disagree, the gate fails
// closed in production (nobody can delete their own account) while every other
// test stays green. Regenerate with:
//
//	node -e 'import("@noble/secp256k1").then(async s => { … })'
//
// against the same fixed scalar (32 zero bytes with 42 in the last).
func TestVerify_AcceptsARealNobleV3Signature(t *testing.T) {
	const (
		uid       = "ztbJ71eLvfmGXw2ydaSPHavfwjva5Bzadutw2tvYDVFVY"
		challenge = "metamesh-identity-v1:delete:ztbJ71eLvfmGXw2ydaSPHavfwjva5Bzadutw2tvYDVFVY:deadbeef"
		sigB64    = "lIiZGL0j9l/w2WXwzwe56cICnusuYdD3oaTuYfFTtNJBR9MolXFPk3014WQeAlvmbX4LfnW+TQz4asqoSwmSAQ=="
	)

	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("fixture is %d bytes, expected noble's 64-byte compact form", len(sig))
	}
	if err := Verify(uid, []byte(challenge), sig); err != nil {
		t.Fatalf("a genuine browser signature must verify: %v", err)
	}

	// And the same signature must not verify over a different challenge — a
	// verifier that ignored the payload would pass the line above too.
	if err := Verify(uid, []byte(challenge+"x"), sig); err == nil {
		t.Fatal("the fixture verified over the wrong payload")
	}
}
