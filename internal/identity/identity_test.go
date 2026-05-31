package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func TestGenerateUIDIsDeterministicFromPubkey(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(id.UID, "z") {
		t.Fatalf("uid missing multibase 'z' prefix: %q", id.UID)
	}
	if id.Curve != CurveSecp256k1 {
		t.Fatalf("curve = %q", id.Curve)
	}
	if len(id.PrivateKeyHex) != 64 {
		t.Fatalf("privkey hex len = %d", len(id.PrivateKeyHex))
	}
	if len(id.PublicKeyHex) != 66 {
		t.Fatalf("compressed pubkey hex len = %d", len(id.PublicKeyHex))
	}

	// Reimporting yields the same uid.
	imported, err := Import(id.PrivateKeyHex)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.UID != id.UID {
		t.Fatalf("uid changed after import roundtrip: %q vs %q", id.UID, imported.UID)
	}
	if imported.PublicKeyHex != id.PublicKeyHex {
		t.Fatalf("pubkey changed after import roundtrip")
	}
}

func TestImportRejectsBadInput(t *testing.T) {
	if _, err := Import(""); err == nil {
		t.Fatal("empty hex should error")
	}
	if _, err := Import("zz"); err == nil {
		t.Fatal("non-hex should error")
	}
	if _, err := Import("00"); err == nil {
		t.Fatal("wrong-length hex should error")
	}
	if _, err := Import(strings.Repeat("00", 32)); err == nil {
		t.Fatal("zero scalar should error")
	}
	// Hex with 0x prefix should work.
	id1, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := Import("0x" + id1.PrivateKeyHex)
	if err != nil {
		t.Fatalf("0x-prefixed hex: %v", err)
	}
	if id1.UID != id2.UID {
		t.Fatal("uid mismatch through 0x prefix")
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.json")

	if Exists(path) {
		t.Fatal("file should not exist yet")
	}
	if got, _ := Load(path); got != nil {
		t.Fatal("Load on missing file should return nil")
	}

	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, id); err != nil {
		t.Fatal(err)
	}
	if !Exists(path) {
		t.Fatal("file should exist after Save")
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.UID != id.UID || loaded.PrivateKeyHex != id.PrivateKeyHex {
		t.Fatalf("roundtrip mismatch: %+v vs %+v", id, loaded)
	}

	// File should be 0600 (private key on disk).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("identity file mode = %o, want 0600", info.Mode().Perm())
	}

	if err := Delete(path); err != nil {
		t.Fatal(err)
	}
	if Exists(path) {
		t.Fatal("file should not exist after Delete")
	}
	// Delete is idempotent.
	if err := Delete(path); err != nil {
		t.Fatalf("Delete on missing file should be nil: %v", err)
	}
}

func TestSignAndVerify(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("hello user data layer")
	sig, err := Sign(id, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) == 0 {
		t.Fatal("empty sig")
	}

	// Verify externally with the public key, matching what meta-watch will do.
	pubBytes, err := hex.DecodeString(id.PublicKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := secp256k1.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatal(err)
	}
	parsedSig, err := ecdsa.ParseDERSignature(sig)
	if err != nil {
		t.Fatalf("parse DER sig: %v", err)
	}
	digest := sha256.Sum256(msg)
	if !parsedSig.Verify(digest[:], pub) {
		t.Fatal("signature did not verify")
	}

	// Flipping a byte must break verification.
	tampered := append([]byte{}, msg...)
	tampered[0] ^= 0x01
	digest2 := sha256.Sum256(tampered)
	if parsedSig.Verify(digest2[:], pub) {
		t.Fatal("signature verified on tampered message")
	}
}

func TestDeriveAEADKeyIsDeterministic(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	k1, err := DeriveAEADKey(id)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeriveAEADKey(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(k1, k2) {
		t.Fatal("AEAD key derivation is non-deterministic")
	}
	if len(k1) != aeadKeyLen {
		t.Fatalf("AEAD key len = %d, want %d", len(k1), aeadKeyLen)
	}

	// Different identity → different key.
	other, _ := Generate()
	k3, _ := DeriveAEADKey(other)
	if bytesEqual(k1, k3) {
		t.Fatal("two distinct identities derived the same AEAD key")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
