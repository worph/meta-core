package storage

import (
	"context"
	"fmt"
	"time"
)

// The one-shot corpus sweep for METADATA_KEYS.md §14.17: the `film` + `tv`
// domains merged into `screen`, and the standalone-vs-serial split they were
// really carrying moved to the new `workForm` key.
//
// # Why a sweep and not a read-side alias
//
// `domain` is **stored, not derived on read** — that is what lets the
// `contentKind` vocabulary stay open (a peer that has never heard of a kind
// still routes the record correctly). The price of storing it is that a
// vocabulary change has to be paid once, in the data: every consumer's filter
// is an exact match, so a record left saying `film` is invisible to a
// `domain:screen` wall, and teaching every consumer to accept both spellings
// forever is exactly the alternation the merge exists to delete.
//
// So the values are rewritten at rest, here, and no alias vocabulary survives
// in query filters. (meta-search additionally normalises on the way into its
// index, because a record can still arrive from a peer that has not been swept
// — that is a courtesy for un-swept peers, not a second source of truth.)
//
// # Contract
//
// Idempotent: a record already carrying `screen` and a `workForm` is skipped,
// so the endpoint is safe to call repeatedly and safe to call on a fresh box.
// Returns the number of records **changed**, not the number examined.

// legacyDomainReplacements maps a retired `domain` value to its replacement.
var legacyDomainReplacements = map[string]string{
	"film": "screen",
	"tv":   "screen",
}

// workFormByContentKind is the `contentKind` → `workForm` table from
// METADATA_KEYS.md §1.
//
// ⚠ Mirror of the Rust table in `meta-feeder-sdk::domain` (and of
// meta-search's copy). It exists here only to backfill the pre-merge corpus —
// meta-core never classifies a record it is given, and nothing else in Go reads
// it. If the tables ever disagree, the writers win: this one is a one-shot.
var workFormByContentKind = map[string]string{
	// screen
	"movie":   "standalone",
	"series":  "serial",
	"episode": "serial",
	// music — an album is a closed work, not an ongoing run.
	"track":      "standalone",
	"album":      "standalone",
	"artist":     "standalone",
	"musicVideo": "standalone",
	"djMix":      "standalone",
	"liveSet":    "standalone",
	// literature
	"book":      "standalone",
	"audiobook": "standalone",
	"comic":     "serial",
	"manga":     "serial",
	"magazine":  "serial",
	// science
	"paper": "standalone",
	// ⚠ `pack` is deliberately absent: a season pack is `serial`, an album
	// release `standalone`, and only the writer knows which. A pack that
	// reaches this sweep without a `workForm` keeps none — inventing one would
	// be a guess written permanently into the record.
}

// domainScreenPlan is one record's pending rewrite. Empty strings mean "leave
// this field alone".
type domainScreenPlan struct {
	hashID   string
	domain   string
	workForm string
}

// MigrateDomainScreen rewrites `domain=film|tv` to `domain=screen` and
// backfills `workForm` from `contentKind` across the whole corpus.
//
// Reads in two batched MGET passes (never a per-record SCAN — a single record
// read costs a full keyspace sweep on this store), then writes only the records
// that actually change.
func (c *Client) MigrateDomainScreen(ctx context.Context) (int, error) {
	plans, err := c.planDomainScreen(ctx)
	if err != nil {
		return 0, err
	}
	// Writes take the client lock themselves, so they happen after the read
	// phase has released it.
	fixed := 0
	for _, p := range plans {
		if p.domain != "" {
			if err := c.SetProperty(p.hashID, "domain", p.domain); err != nil {
				return fixed, fmt.Errorf("set domain on %s: %w", p.hashID, err)
			}
		}
		if p.workForm != "" {
			if err := c.SetProperty(p.hashID, "workForm", p.workForm); err != nil {
				return fixed, fmt.Errorf("set workForm on %s: %w", p.hashID, err)
			}
		}
		fixed++
	}
	return fixed, nil
}

// planDomainScreen is the read half: what would change, without changing it.
func (c *Client) planDomainScreen(ctx context.Context) ([]domainScreenPlan, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	hashIDs, err := c.client.SMembers(ctx, c.buildIndexKey()).Result()
	if err != nil {
		return nil, fmt.Errorf("smembers index: %w", err)
	}
	if len(hashIDs) == 0 {
		return nil, nil
	}

	// Three keys per record, in a fixed order so the MGET response decodes by
	// position: keys[i*3+n] belongs to hashIDs[i].
	const fieldsPerFile = 3
	fields := [fieldsPerFile]string{"domain", "workForm", "contentKind"}
	keys := make([]string, 0, len(hashIDs)*fieldsPerFile)
	for _, h := range hashIDs {
		prefix := c.buildKeyPrefix(h)
		for _, f := range fields {
			keys = append(keys, prefix+f)
		}
	}

	// Chunked so one MGET never carries the whole corpus.
	const chunk = 3000
	values := make([]interface{}, 0, len(keys))
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		got, err := c.client.MGet(ctx, keys[start:end]...).Result()
		if err != nil {
			return nil, fmt.Errorf("mget: %w", err)
		}
		values = append(values, got...)
	}

	str := func(i int) string {
		if i >= len(values) {
			return ""
		}
		s, _ := values[i].(string)
		return s
	}

	plans := make([]domainScreenPlan, 0)
	for i, h := range hashIDs {
		base := i * fieldsPerFile
		domain, workForm, contentKind := str(base), str(base+1), str(base+2)

		plan := domainScreenPlan{hashID: h}
		if replacement, ok := legacyDomainReplacements[domain]; ok {
			plan.domain = replacement
		}
		// A stored `workForm` is authoritative — a `pack` writer knew something
		// the table cannot. Only an absent one is filled in.
		if workForm == "" {
			if form, ok := workFormByContentKind[contentKind]; ok {
				plan.workForm = form
			}
		}
		if plan.domain != "" || plan.workForm != "" {
			plans = append(plans, plan)
		}
	}
	return plans, nil
}

// MigrateDomainScreenWithTimeout is the convenience wrapper used by the API
// handler. Generous timeout: the read phase is two batched passes, but the
// write phase is one round-trip per changed field.
func (c *Client) MigrateDomainScreenWithTimeout() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return c.MigrateDomainScreen(ctx)
}
