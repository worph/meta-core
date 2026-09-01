package storage

import (
	"context"
	"testing"
)

// seedRecord writes one record's classification fields straight through
// SetProperty, which is what a real writer uses (it maintains the field index
// and the root index the sweep reads from).
func seedRecord(t *testing.T, c *Client, hashID string, fields map[string]string) {
	t.Helper()
	for k, v := range fields {
		if err := c.SetProperty(hashID, k, v); err != nil {
			t.Fatalf("seed %s/%s: %v", hashID, k, err)
		}
	}
}

// The merge (METADATA_KEYS.md §14.17) is paid in the data: `domain` is stored,
// not derived on read, so a record left saying `film` is invisible to a
// `domain:screen` wall. This is the sweep that pays it.
func TestMigrateDomainScreen_RewritesLegacyDomainsAndBackfillsWorkForm(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedRecord(t, c, "rec-movie", map[string]string{
		"domain":      "film",
		"contentKind": "movie",
	})
	seedRecord(t, c, "rec-episode", map[string]string{
		"domain":      "tv",
		"contentKind": "episode",
	})
	// Already current, and already carries both keys: must be left alone.
	seedRecord(t, c, "rec-current", map[string]string{
		"domain":      "screen",
		"contentKind": "series",
		"workForm":    "serial",
	})
	// A different domain entirely — the sweep must not touch it, but it is
	// still owed the `workForm` backfill.
	seedRecord(t, c, "rec-book", map[string]string{
		"domain":      "literature",
		"contentKind": "book",
	})

	fixed, err := c.MigrateDomainScreen(context.Background())
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if fixed != 3 {
		t.Errorf("fixed = %d, want 3 (the two legacy rows + the book's backfill)", fixed)
	}

	for _, tc := range []struct {
		hashID   string
		domain   string
		workForm string
	}{
		{"rec-movie", "screen", "standalone"},
		{"rec-episode", "screen", "serial"},
		{"rec-current", "screen", "serial"},
		{"rec-book", "literature", "standalone"},
	} {
		m, err := c.GetMetadataFlat(tc.hashID)
		if err != nil {
			t.Fatalf("read %s: %v", tc.hashID, err)
		}
		if m["domain"] != tc.domain {
			t.Errorf("%s domain = %q, want %q", tc.hashID, m["domain"], tc.domain)
		}
		if m["workForm"] != tc.workForm {
			t.Errorf("%s workForm = %q, want %q", tc.hashID, m["workForm"], tc.workForm)
		}
	}
}

// A second pass must find nothing to do — the endpoint is called by hand on
// each box, and "run it twice" must not mean "write the whole corpus twice".
func TestMigrateDomainScreen_IsIdempotent(t *testing.T) {
	c, _ := newTestClient(t, "mm:")

	seedRecord(t, c, "rec-1", map[string]string{
		"domain":      "tv",
		"contentKind": "episode",
	})

	if _, err := c.MigrateDomainScreen(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	fixed, err := c.MigrateDomainScreen(context.Background())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if fixed != 0 {
		t.Errorf("second pass fixed = %d, want 0", fixed)
	}
}

// `pack` is the one kind neither routing table can decide — a season pack is
// `serial`, an album release `standalone`. A pack that reaches the sweep
// without a `workForm` must keep none: inventing one writes a guess into the
// record permanently.
func TestMigrateDomainScreen_NeverGuessesAPacksWorkForm(t *testing.T) {
	c, _ := newTestClient(t, "")

	seedRecord(t, c, "rec-pack", map[string]string{
		"domain":      "tv",
		"contentKind": "pack",
	})
	// A writer that DID know keeps what it wrote.
	seedRecord(t, c, "rec-album", map[string]string{
		"domain":      "music",
		"contentKind": "pack",
		"workForm":    "standalone",
	})

	if _, err := c.MigrateDomainScreen(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	m, err := c.GetMetadataFlat("rec-pack")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m["domain"] != "screen" {
		t.Errorf("domain = %q, want screen (the domain WAS knowable)", m["domain"])
	}
	if _, ok := m["workForm"]; ok {
		t.Errorf("workForm = %q, want absent", m["workForm"])
	}

	a, err := c.GetMetadataFlat("rec-album")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if a["workForm"] != "standalone" {
		t.Errorf("workForm = %q, want standalone (writer-supplied, never overwritten)", a["workForm"])
	}
}
