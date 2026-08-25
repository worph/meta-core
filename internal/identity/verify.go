package identity

// Signature verification against a uid, and the challenge vocabulary that
// makes a signature mean "I hold this account's private key" rather than
// "meta-core signed something for me".
//
// The asymmetry that motivates this file: meta-core *holds* every private key,
// so it can sign as anybody. A signature is therefore only proof of possession
// if the payload is one meta-core refuses to sign on request — hence
// ChallengeDomain, which POST /api/identity/sign rejects outright. Without that
// refusal an attacker mints their own authorisation in one extra call and the
// whole gate is decoration.
//
// Verification itself needs no stored state: a uid *is* the public key
// ("z" + base58btc(33-byte compressed sec1)), so the pubkey is recovered from
// the uid the caller names. Mirrors `verify_local` / `verify_local_compact` in
// meta-watch's src/record/sign.rs — same curve, same digest, both signature
// encodings.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/mr-tron/base58"
)

// ChallengeDomain prefixes every byte string that authorises an account
// operation. POST /api/identity/sign and /sign-batch refuse payloads carrying
// it, so the only way to produce one is to hold the key yourself.
//
// Versioned because it is a wire commitment: changing it invalidates every
// challenge in flight, which is harmless, but changing it *silently* would let
// an old-domain payload keep working. Bump it deliberately.
const ChallengeDomain = "metamesh-identity-v1:"

// Action is the operation a challenge authorises. A challenge minted to reveal
// a key must not also delete the account, so the action is inside the signed
// bytes and is re-checked at redemption.
type Action string

const (
	ActionReveal Action = "reveal"
	ActionDelete Action = "delete"
)

// ValidAction reports whether a is an action a challenge may be issued for.
func ValidAction(a Action) bool {
	return a == ActionReveal || a == ActionDelete
}

// ValidUID exposes the uid shape check to callers outside this package (the
// API layer, which must reject a hostile uid before it reaches a Redis SCAN
// pattern or a file path).
func ValidUID(uid string) bool { return validUID(uid) }

// IsChallengePayload reports whether b is (or claims to be) an account
// authorisation payload. Used by the signing endpoints to refuse minting one.
//
// Deliberately a prefix test on raw bytes rather than a parse: anything that
// merely *looks* like a challenge is refused, so a malformed or future-version
// payload fails closed instead of being signed.
func IsChallengePayload(b []byte) bool {
	return strings.HasPrefix(string(b), ChallengeDomain)
}

// PubkeyFromUID recovers the compressed public key a uid encodes.
func PubkeyFromUID(uid string) (*secp256k1.PublicKey, error) {
	if !validUID(uid) {
		return nil, fmt.Errorf("invalid uid: %q", uid)
	}
	if !strings.HasPrefix(uid, "z") {
		return nil, errors.New("uid is not multibase base58btc (missing 'z' prefix)")
	}
	raw, err := base58.Decode(uid[1:])
	if err != nil {
		return nil, fmt.Errorf("uid is not valid base58: %w", err)
	}
	if len(raw) != 33 {
		return nil, fmt.Errorf("uid decodes to %d bytes, want a 33-byte compressed pubkey", len(raw))
	}
	pub, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("uid is not a point on secp256k1: %w", err)
	}
	return pub, nil
}

// Verify checks sig over SHA-256(payload) against the public key inside uid.
//
// Accepts both encodings in circulation: DER (what Sign emits, and what User
// Data Layer records carry) and 64-byte compact r‖s (what the browser emits —
// noble-secp256k1 v3 dropped DER, and meta-watch's sign-in already sends
// compact). Rejecting either would break one of the two callers.
func Verify(uid string, payload, sig []byte) error {
	pub, err := PubkeyFromUID(uid)
	if err != nil {
		return err
	}
	parsed, err := parseSignature(sig)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if !parsed.Verify(digest[:], pub) {
		return errors.New("signature does not verify for this uid")
	}
	return nil
}

func parseSignature(sig []byte) (*ecdsa.Signature, error) {
	if len(sig) == 64 {
		var r, s secp256k1.ModNScalar
		// SetByteSlice reports overflow (>= curve order), which is not a valid
		// scalar. A zero r or s is likewise never produced by a real signer.
		if r.SetByteSlice(sig[:32]) || r.IsZero() {
			return nil, errors.New("compact signature has an invalid r")
		}
		if s.SetByteSlice(sig[32:]) || s.IsZero() {
			return nil, errors.New("compact signature has an invalid s")
		}
		return ecdsa.NewSignature(&r, &s), nil
	}
	parsed, err := ecdsa.ParseDERSignature(sig)
	if err != nil {
		return nil, fmt.Errorf("signature is neither 64-byte compact nor valid DER: %w", err)
	}
	return parsed, nil
}

// NewChallengeText mints the literal string a client signs. The uid and action
// are inside it, so a signature cannot be lifted from one account or operation
// onto another even if the nonce leaks.
func NewChallengeText(uid string, action Action) (string, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("read nonce: %w", err)
	}
	return ChallengeDomain + string(action) + ":" + uid + ":" + hex.EncodeToString(nonce), nil
}

// ChallengeMatches reports whether text is a well-formed challenge for exactly
// this uid and action. The store already keys on the full text, so this is a
// belt-and-braces re-read of the signed bytes themselves: it is the signed
// content, not the lookup key, that a verifier must agree with.
//
// Compared in constant time — the text is a bearer secret until redeemed.
func ChallengeMatches(text, uid string, action Action) bool {
	want := ChallengeDomain + string(action) + ":" + uid + ":"
	if len(text) <= len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(text[:len(want)]), []byte(want)) == 1
}
