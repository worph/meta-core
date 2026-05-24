package storage

import (
	"strings"
	"testing"
	"time"
)

func TestNewUUID_Shape(t *testing.T) {
	id, err := NewUUID()
	if err != nil {
		t.Fatalf("NewUUID: %v", err)
	}
	if len(id) != 26 {
		t.Errorf("expected 26 chars, got %d (%q)", len(id), id)
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i, c := range id {
		if !strings.ContainsRune(alphabet, c) {
			t.Errorf("char %d (%q) not in Crockford alphabet (uuid=%q)", i, c, id)
		}
	}
}

func TestNewUUID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id, err := NewUUID()
		if err != nil {
			t.Fatalf("NewUUID at i=%d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("collision after %d mints: %q", i+1, id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewUUID_TimeSortable(t *testing.T) {
	// UUIDv7 has a millisecond timestamp prefix; consecutive mints in the
	// same millisecond may not be strictly ordered (random tail), but
	// mints across milliseconds are. Just check that an early ID
	// lexicographically sorts before a later one when there's a real
	// time gap. Doesn't test the no-gap case — the v7 spec only
	// guarantees ordering across millisecond boundaries.
	a, _ := NewUUID()
	time.Sleep(2 * time.Millisecond)
	b, _ := NewUUID()
	if a >= b {
		t.Errorf("expected %q < %q (UUIDv7 should be roughly time-sortable)", a, b)
	}
}

func TestCIDTokenFromField(t *testing.T) {
	cases := []struct {
		field, value string
		want         string
	}{
		{"midhash256", "bafk", "midhash256:bafk"},
		{"cid_sha256", "bafk", "sha256:bafk"},
		{"cid_ipfs", "bafy", "ipfs:bafy"},
		{"cid_sha256", "sha256:bafk", "sha256:bafk"}, // already prefixed
		{"cid_ipfs", "ipfs:bafy", "ipfs:bafy"},       // already prefixed
		{"title", "Inception", ""},                    // non-CID field
		{"tmdb/title", "Inception", ""},               // non-CID nested
		{"cid_", "bafk", ""},                          // empty algo
		{"midhash256", "", ""},                        // empty value
		{"cid_sha256", "", ""},                        // empty value
	}
	for _, tc := range cases {
		if got := cidTokenFromField(tc.field, tc.value); got != tc.want {
			t.Errorf("cidTokenFromField(%q, %q) = %q, want %q",
				tc.field, tc.value, got, tc.want)
		}
	}
}
