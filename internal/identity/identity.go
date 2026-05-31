// Package identity manages the user-controlled signing identity used by the
// User Data Layer (see docs/project-architecture/user-data-layer.md).
//
// The keypair is secp256k1 (D1: wallet forward-compat). The uid is the
// multibase base58btc encoding of the 33-byte compressed pubkey, prefixed
// with "z" per multibase convention.
//
// Storage is plaintext JSON at {META_CORE_PATH}/identity/identity.json,
// matching the trust model of the rest of /meta-core. Signing happens
// in-process; meta-watch calls POST /api/identity/sign rather than holding
// the private key itself.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/mr-tron/base58"
	"golang.org/x/crypto/hkdf"
)

const (
	CurveSecp256k1 = "secp256k1"

	// HKDF parameters for deriving the AEAD key from the private key.
	// Stable across versions — changing these is a breaking change for
	// encrypted records on disk.
	aeadKDFSalt = "metamesh-uds-v1"
	aeadKDFInfo = "aead-key"
	aeadKeyLen  = 32
)

// Identity is the persisted form of a user's keypair.
//
// PrivateKeyHex is the 32-byte secp256k1 scalar, lower-case hex, no prefix.
// PublicKeyHex is the 33-byte compressed pubkey, lower-case hex, no prefix.
type Identity struct {
	UID           string `json:"uid"`
	Curve         string `json:"curve"`
	PublicKeyHex  string `json:"publicKeyHex"`
	PrivateKeyHex string `json:"privateKeyHex"`
	CreatedAt     int64  `json:"createdAt"`
}

// Generate creates a fresh secp256k1 identity.
func Generate() (*Identity, error) {
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	return identityFromPrivKey(priv), nil
}

// Import parses an externally-provided 32-byte secp256k1 scalar and derives
// the matching pubkey. Accepts hex with or without a "0x" prefix.
func Import(privateKeyHex string) (*Identity, error) {
	if len(privateKeyHex) >= 2 && (privateKeyHex[:2] == "0x" || privateKeyHex[:2] == "0X") {
		privateKeyHex = privateKeyHex[2:]
	}
	raw, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(raw))
	}
	priv := secp256k1.PrivKeyFromBytes(raw)
	// Reject the zero scalar (secp256k1 lib does not reject it on its own).
	pubBytes := priv.PubKey().SerializeCompressed()
	if isAllZero(raw) || isAllZero(pubBytes) {
		return nil, errors.New("private key is the curve identity (zero); refusing")
	}
	return identityFromPrivKey(priv), nil
}

func identityFromPrivKey(priv *secp256k1.PrivateKey) *Identity {
	pubBytes := priv.PubKey().SerializeCompressed()
	return &Identity{
		UID:           uidFromCompressedPubkey(pubBytes),
		Curve:         CurveSecp256k1,
		PublicKeyHex:  hex.EncodeToString(pubBytes),
		PrivateKeyHex: hex.EncodeToString(priv.Serialize()),
		CreatedAt:     time.Now().UTC().Unix(),
	}
}

// uidFromCompressedPubkey encodes the pubkey as a multibase base58btc
// string ("z" prefix). The encoding is deterministic and matches MetaMesh
// CID conventions.
func uidFromCompressedPubkey(pub []byte) string {
	return "z" + base58.Encode(pub)
}

// Load reads the identity file. Returns (nil, nil) if it does not exist —
// callers distinguish "no identity" from real errors.
func Load(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read identity file: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("parse identity file: %w", err)
	}
	if id.Curve != CurveSecp256k1 {
		return nil, fmt.Errorf("unsupported curve on disk: %q", id.Curve)
	}
	return &id, nil
}

// Save writes the identity atomically (tmp + rename), matching the pattern
// used by discovery/service.go.
func Save(path string, id *Identity) error {
	if id == nil {
		return errors.New("nil identity")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir identity dir: %w", err)
	}
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename tmp identity: %w", err)
	}
	return nil
}

// Delete removes the identity file. Idempotent.
func Delete(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Exists reports whether an identity file is present.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Sign produces an ECDSA-secp256k1 signature over SHA-256(bytes). The output
// is DER-encoded — the wire format we commit to for User Data Layer records.
func Sign(id *Identity, bytes []byte) ([]byte, error) {
	if id == nil {
		return nil, errors.New("no identity loaded")
	}
	raw, err := hex.DecodeString(id.PrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("private key length %d", len(raw))
	}
	priv := secp256k1.PrivKeyFromBytes(raw)
	digest := sha256.Sum256(bytes)
	sig := ecdsa.Sign(priv, digest[:])
	return sig.Serialize(), nil
}

// DeriveAEADKey derives the symmetric key used to encrypt private-tier
// User Data Layer records. The KDF parameters (salt, info, length) are
// fixed for the v1 wire format.
func DeriveAEADKey(id *Identity) ([]byte, error) {
	if id == nil {
		return nil, errors.New("no identity loaded")
	}
	raw, err := hex.DecodeString(id.PrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	r := hkdf.New(sha256.New, raw, []byte(aeadKDFSalt), []byte(aeadKDFInfo))
	out := make([]byte, aeadKeyLen)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return out, nil
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// Ensure rand.Reader is referenced so dependency-stripped builds don't
// accidentally remove the crypto/rand import that secp256k1.GeneratePrivateKey
// relies on transitively.
var _ = rand.Reader
