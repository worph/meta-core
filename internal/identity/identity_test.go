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

func TestAccountStoreRoundtrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")

	// Empty store.
	list, err := List(dir)
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty store, got %d", len(list))
	}

	a, _ := Generate()
	b, _ := Generate()
	if err := SaveAccount(dir, a); err != nil {
		t.Fatal(err)
	}
	if err := SaveAccount(dir, b); err != nil {
		t.Fatal(err)
	}

	if !ExistsByUID(dir, a.UID) || !ExistsByUID(dir, b.UID) {
		t.Fatal("both accounts should exist")
	}

	list, err = List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(list))
	}

	got, err := LoadByUID(dir, a.UID)
	if err != nil || got == nil || got.PrivateKeyHex != a.PrivateKeyHex {
		t.Fatalf("LoadByUID roundtrip failed: %+v err=%v", got, err)
	}

	// Missing uid → (nil, nil).
	if got, err := LoadByUID(dir, "zdoesnotexist"); err != nil || got != nil {
		t.Fatalf("LoadByUID on missing should be (nil,nil): %+v %v", got, err)
	}

	// Delete one; the other remains. Delete is idempotent.
	if err := DeleteByUID(dir, a.UID); err != nil {
		t.Fatal(err)
	}
	if ExistsByUID(dir, a.UID) {
		t.Fatal("account should be gone after delete")
	}
	if err := DeleteByUID(dir, a.UID); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
	list, _ = List(dir)
	if len(list) != 1 || list[0].UID != b.UID {
		t.Fatalf("expected only b remaining, got %+v", list)
	}
}

func TestAccountFilePathRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", "z..", strings.Repeat("z", 200)} {
		if _, err := accountFilePath("/tmp/x", bad); err == nil {
			t.Fatalf("expected rejection of uid %q", bad)
		}
	}
	// A real uid is accepted.
	id, _ := Generate()
	if _, err := accountFilePath("/tmp/x", id.UID); err != nil {
		t.Fatalf("real uid rejected: %v", err)
	}
}

func TestMigrateLegacy(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "identity.json")
	accounts := filepath.Join(base, "accounts")

	// No legacy file → no-op.
	if migrated, err := MigrateLegacy(legacy, accounts); err != nil || migrated {
		t.Fatalf("expected no-op migration, got migrated=%v err=%v", migrated, err)
	}

	id, _ := Generate()
	if err := Save(legacy, id); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateLegacy(legacy, accounts)
	if err != nil || !migrated {
		t.Fatalf("expected migration to run: migrated=%v err=%v", migrated, err)
	}
	if Exists(legacy) {
		t.Fatal("legacy file should be removed after migration")
	}
	got, err := LoadByUID(accounts, id.UID)
	if err != nil || got == nil || got.UID != id.UID {
		t.Fatalf("migrated account not found: %+v err=%v", got, err)
	}

	// Second run is a clean no-op (legacy already gone).
	if migrated, err := MigrateLegacy(legacy, accounts); err != nil || migrated {
		t.Fatalf("second migration should no-op: migrated=%v err=%v", migrated, err)
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
