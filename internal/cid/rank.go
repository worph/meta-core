// Package cid centralizes CID-format helpers: ranking by utility for
// external presentation, normalizing prefixed CID tokens, and the rules for
// picking which CID is the canonical externally-visible identifier of a
// file.
//
// A "CID token" in this codebase is the prefixed form "<algorithm>:<value>",
// e.g. "midhash256:bafkr4ih...", "sha256:bafkrei...", "ipfs:bafy...". The
// prefix makes the type recoverable without parsing the multicodec, which is
// what the reverse index (cid:<token> → uuid), the per-root cids set, and
// the canonical_cid field all rely on.
package cid

import "strings"

// Rank returns a score for a CID token. Higher = more preferred as the
// public-facing "canonical" identifier for a file. Unknown algorithms rank
// below all known ones (0) so a plugin that invents a new digest type
// can't accidentally outrank an established one until it's registered here.
//
// Ordering rationale (utility-weighted, NOT cryptographic-strength):
//   - IPFS CIDs win because the network around them — gateways, swarm,
//     content-addressing tooling — has the broadest reach. A cryptographically
//     equivalent sha256 in a non-CID form has less downstream value.
//   - sha256 / sha3 follow: standard, widely recognized, no platform lock-in.
//   - BitTorrent infohashes (btih) outrank midhash256 not for strength
//     (v1 is sha1, weaker than midhash256's sha256 inner) but for swarm
//     interop — a btih makes a file discoverable on existing torrent
//     infrastructure that knows nothing about MetaMesh.
//   - midhash256 is the floor. Internally meaningful, externally invisible.
func Rank(token string) int {
	switch AlgorithmOf(token) {
	case "ipfs":
		return 40
	case "sha256", "sha3", "sha3-256", "sha3-384", "sha3-512":
		return 30
	case "btih", "btih-v2":
		return 20
	case "midhash256":
		return 10
	default:
		return 0
	}
}

// AlgorithmOf returns the algorithm prefix of a CID token, or "" if the
// token is malformed (no colon, or empty prefix).
func AlgorithmOf(token string) string {
	colon := strings.IndexByte(token, ':')
	if colon <= 0 {
		return ""
	}
	return token[:colon]
}

// Token assembles a CID token from its algorithm and raw value. Mirror of
// AlgorithmOf — callers writing to the reverse index, cids set, or
// canonical_cid field route through this so the format stays consistent.
func Token(algorithm, raw string) string {
	if algorithm == "" || raw == "" {
		return ""
	}
	return algorithm + ":" + raw
}

// ValueOf returns the raw value portion of a CID token (everything after
// the first colon). Returns "" for malformed tokens.
func ValueOf(token string) string {
	colon := strings.IndexByte(token, ':')
	if colon <= 0 || colon == len(token)-1 {
		return ""
	}
	return token[colon+1:]
}

// Better picks the higher-ranked of two tokens. On ties, returns the
// lexicographically smaller one — gives the reconciler a deterministic
// answer so an unchanged canonical_cid doesn't churn when peers add the
// same digest under a different ordering.
func Better(a, b string) string {
	ra, rb := Rank(a), Rank(b)
	if ra > rb {
		return a
	}
	if rb > ra {
		return b
	}
	if a <= b {
		return a
	}
	return b
}
