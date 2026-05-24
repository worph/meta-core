package cid

import "testing"

func TestRank_Ordering(t *testing.T) {
	cases := []struct {
		token string
		want  int
	}{
		{"ipfs:bafy", 40},
		{"sha256:bafk", 30},
		{"sha3-256:abc", 30},
		{"btih:def", 20},
		{"btih-v2:def", 20},
		{"midhash256:bafk", 10},
		{"unknown:xyz", 0},
		{"", 0},
		{"bare-no-colon", 0},
	}
	for _, tc := range cases {
		if got := Rank(tc.token); got != tc.want {
			t.Errorf("Rank(%q) = %d, want %d", tc.token, got, tc.want)
		}
	}
}

func TestRank_StrictOrdering(t *testing.T) {
	// The exact numbers don't matter, but the inequality chain does — this
	// is what the canonical_cid reconciler relies on.
	ipfs := Rank("ipfs:x")
	sha := Rank("sha256:x")
	btih := Rank("btih:x")
	mid := Rank("midhash256:x")
	unknown := Rank("unknown:x")

	if !(ipfs > sha && sha > btih && btih > mid && mid > unknown) {
		t.Fatalf("rank ordering broken: ipfs=%d sha=%d btih=%d mid=%d unknown=%d",
			ipfs, sha, btih, mid, unknown)
	}
}

func TestAlgorithmOf(t *testing.T) {
	cases := map[string]string{
		"ipfs:bafy":       "ipfs",
		"sha256:bafk":     "sha256",
		"midhash256:abc":  "midhash256",
		"":               "",
		"no-colon":        "",
		":leading-colon":  "", // empty algo not allowed
	}
	for in, want := range cases {
		if got := AlgorithmOf(in); got != want {
			t.Errorf("AlgorithmOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToken(t *testing.T) {
	if got := Token("sha256", "abc"); got != "sha256:abc" {
		t.Errorf("Token(sha256, abc) = %q", got)
	}
	if got := Token("", "abc"); got != "" {
		t.Errorf("Token with empty algo should return empty, got %q", got)
	}
	if got := Token("sha256", ""); got != "" {
		t.Errorf("Token with empty value should return empty, got %q", got)
	}
}

func TestBetter(t *testing.T) {
	// IPFS beats sha256
	if got := Better("ipfs:a", "sha256:b"); got != "ipfs:a" {
		t.Errorf("Better(ipfs, sha256) = %q, want ipfs:a", got)
	}
	if got := Better("sha256:b", "ipfs:a"); got != "ipfs:a" {
		t.Errorf("Better(sha256, ipfs) = %q, want ipfs:a (order shouldn't matter)", got)
	}
	// Tie → lexicographically smaller wins, deterministic
	if got := Better("ipfs:b", "ipfs:a"); got != "ipfs:a" {
		t.Errorf("Better(ipfs:b, ipfs:a) = %q, want ipfs:a", got)
	}
	if got := Better("ipfs:a", "ipfs:b"); got != "ipfs:a" {
		t.Errorf("Better(ipfs:a, ipfs:b) = %q, want ipfs:a", got)
	}
}
