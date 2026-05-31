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

func TestCIDFromKeysetField(t *testing.T) {
	cases := []struct {
		field string
		want  string
	}{
		{"cids/bafkmidhash", "bafkmidhash"},
		{"cids/baejbeisha256", "baejbeisha256"},
		{"cids/bafyipfs", "bafyipfs"},
		{"title", ""},          // non-CID field
		{"tmdb/title", ""},     // non-CID nested
		{"cid_sha2-256", ""},   // legacy flat field — no longer recognized
		{"midhash256", ""},     // legacy named field — no longer recognized
		{"cids", ""},           // prefix without a member
	}
	for _, tc := range cases {
		if got := cidFromKeysetField(tc.field); got != tc.want {
			t.Errorf("cidFromKeysetField(%q) = %q, want %q", tc.field, got, tc.want)
		}
	}
}
