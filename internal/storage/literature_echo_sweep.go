package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/metazla/meta-core/internal/cid"
)

// The one-shot corpus sweep for meta-read `docs/reading-model.md` §7:
// identity and position keys are resolved, never copied from a query, and a
// card carries no position keys and no `workcid`.
//
// # Why a sweep
//
// A feeder answering a `workcid:` / `anilistid:` query could stamp the query's
// own filter values onto what it returned. The gateway persists whatever passes
// the filter, so the copy became a permanent binding — and stored records never
// pick up the writer fix: a covered `(upstream, query)` never reaches the
// feeder again, and the streaming dedup keeps meta-core's copy over a fresh
// one. So the copies already at rest are removed here, once.
//
// What the copies cost: an `anilistid` on a MangaDex site card claims an
// AniList binding nobody resolved, and meta-read reads a bare `anilistid` as an
// anchor — the site card then competes with (and can hide) the real AniList
// card for the same work. A `workcid` or `chapterNumber` on a card makes a work
// join and sort like a position inside one.
//
// # What is swept, and what deliberately is not
//
//   - Only `domain=literature` + `fileType=card` records. Nothing else is read
//     for writing, whatever keys it carries.
//   - `workcid`, `chapterNumber`, `volumeNumber`, `chapterStart`, `chapterEnd`
//     go from **every** such card: a card is a work, never a position in one,
//     so no legitimate writer puts them there.
//   - `anilistid` goes only from a **site card** — a card whose own `cids/`
//     key-set holds a site-form card locator and no locator of any other form.
//     On an anchor card the id *is* the card's identity (its locator is
//     `("anilist", …)`). A card holding both forms (a cross-source merge) keeps
//     it: which half the id belongs to is not recoverable at rest, and leaving
//     a stamp is the safe direction.
//   - **Chapters are never swept.** A bare `anilistid` on a chapter is graded
//     Matched by rule, and a query-copied stamp is indistinguishable at rest
//     from a title-matched one — deleting would drop real bindings with the
//     fake ones.
//
// # Contract
//
// Dry-run unless `apply`: the sweep deletes keys, so the default call only
// reports. Idempotent: a swept record plans nothing on the next pass, so the
// endpoint is safe to call repeatedly and on a fresh box.

// literatureEchoAnchorSources are the card-locator sources that name an id-bag
// work (an anchor), never a site.
//
// ⚠ Mirror of meta-read `card_cid::is_host_form`. meta-read spells out only
// `anilist` and `openlibrary`; the others carry no '.', so the '.' test already
// excludes them and the two rules agree on every locator minted today. They are
// listed so the rule reads on its own here.
var literatureEchoAnchorSources = map[string]bool{
	"anilist":     true,
	"openlibrary": true,
	"tmdb":        true,
	"imdb":        true,
	"musicbrainz": true,
	"mal":         true,
}

// literatureCardEchoFields are removed from every literature card carrying
// them — position keys and the work reference, none of which a card may hold.
var literatureCardEchoFields = []string{
	"workcid",
	"chapterNumber",
	"volumeNumber",
	"chapterStart",
	"chapterEnd",
}

// literatureEchoReadFields is the per-record MGET layout: the two gate fields,
// then `anilistid`, then the echo fields in literatureCardEchoFields order.
var literatureEchoReadFields = append(
	[]string{"domain", "fileType", "anilistid"},
	literatureCardEchoFields...,
)

// LiteratureEchoSweep is the result of one sweep pass. Planned and Fixed count
// records; ByKey counts fields per key — removed on an apply pass, that would
// be removed on a dry run. ByKey is never nil so it serialises as `{}`.
type LiteratureEchoSweep struct {
	Planned int
	Fixed   int
	ByKey   map[string]int
}

// literatureEchoPlan is one record's pending deletes, in a fixed order
// (`anilistid` first, then literatureCardEchoFields order).
type literatureEchoPlan struct {
	hashID string
	fields []string
}

// isSiteFormCardSource is the site-form rule on an already-decoded source: not
// an anchor source, and shaped like a host.
func isSiteFormCardSource(source string) bool {
	return !literatureEchoAnchorSources[source] && strings.Contains(source, ".")
}

// isSiteFormCardLocator reports whether a bare CID is a card locator (0x1007)
// whose source is a site (`mangadex.org`), not an anchor (`anilist`).
func isSiteFormCardLocator(cidStr string) bool {
	source, _, err := cid.DecodeCard(cidStr)
	return err == nil && isSiteFormCardSource(source)
}

// isSiteCard reports whether a card's field names prove it a site card: at
// least one site-form `cids/` card locator and no card locator of another form.
// Non-card members (a content CID) say nothing about which work the card is and
// are ignored. An empty field list — a record with no field-name index — is
// never a site card, which keeps its `anilistid`; see planLiteratureEchoes.
func isSiteCard(fields []string) bool {
	site := false
	for _, f := range fields {
		member := cidFromKeysetField(f)
		if member == "" {
			continue
		}
		source, _, err := cid.DecodeCard(member)
		if err != nil {
			continue
		}
		if !isSiteFormCardSource(source) {
			return false
		}
		site = true
	}
	return site
}

// SweepLiteratureEchoes removes query echoes stored on literature cards (see
// the file comment). With apply=false it only plans.
//
// Reads in batched passes (never a per-record SCAN — a single record read
// costs a full keyspace sweep on this store), then deletes only the planned
// fields, through DeleteProperty so the field-name index and the search-dirty
// set stay consistent.
func (c *Client) SweepLiteratureEchoes(ctx context.Context, apply bool) (LiteratureEchoSweep, error) {
	res := LiteratureEchoSweep{ByKey: map[string]int{}}

	plans, err := c.planLiteratureEchoes(ctx)
	if err != nil {
		return res, err
	}
	res.Planned = len(plans)

	if !apply {
		for _, p := range plans {
			for _, f := range p.fields {
				res.ByKey[f]++
			}
		}
		return res, nil
	}

	// Writes take the client lock themselves, so they happen after the read
	// phase has released it. ByKey counts what was actually deleted, so a
	// failure midway reports the true partial state.
	for _, p := range plans {
		// DeleteProperty carries its own per-call timeout; checking ctx here is
		// what makes the wrapper's deadline bound the whole write phase.
		if err := ctx.Err(); err != nil {
			return res, err
		}
		for _, f := range p.fields {
			if err := c.DeleteProperty(p.hashID, f); err != nil {
				return res, fmt.Errorf("delete %s on %s: %w", f, p.hashID, err)
			}
			res.ByKey[f]++
		}
		res.Fixed++
	}
	return res, nil
}

// planLiteratureEchoes is the read half: what would be deleted, without
// deleting it.
func (c *Client) planLiteratureEchoes(ctx context.Context) ([]literatureEchoPlan, error) {
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

	// Pass 1: the gate and echo fields for every record, in a fixed order so
	// the MGET response decodes by position: keys[i*per+n] belongs to
	// hashIDs[i].
	per := len(literatureEchoReadFields)
	keys := make([]string, 0, len(hashIDs)*per)
	for _, h := range hashIDs {
		prefix := c.buildKeyPrefix(h)
		for _, f := range literatureEchoReadFields {
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

	// A nil slot is an absent key. A present key holding "" is still an echo
	// and still goes, so presence — not the value — decides.
	get := func(i int) (string, bool) {
		if i >= len(values) || values[i] == nil {
			return "", false
		}
		s, ok := values[i].(string)
		return s, ok
	}

	type candidate struct {
		hashID     string
		hasAnilist bool
		echoes     []string
	}
	var cands []candidate
	var probe []string // candidates whose anilistid needs their cids/ key-set
	for i, h := range hashIDs {
		base := i * per
		domain, _ := get(base)
		fileType, _ := get(base + 1)
		if domain != "literature" || fileType != "card" {
			continue
		}
		cand := candidate{hashID: h}
		_, cand.hasAnilist = get(base + 2)
		for j, f := range literatureCardEchoFields {
			if _, ok := get(base + 3 + j); ok {
				cand.echoes = append(cand.echoes, f)
			}
		}
		if !cand.hasAnilist && len(cand.echoes) == 0 {
			continue
		}
		if cand.hasAnilist {
			probe = append(probe, h)
		}
		cands = append(cands, cand)
	}

	// Pass 2: the field-name index of only the cards carrying an `anilistid`
	// — the `cids/` members are field NAMES, so the index is where they are
	// listed. Pipelined per chunk; still no SCAN.
	//
	// A card with no index (never touched since before the index existed, and
	// missed by the startup WarmFieldIndexes pass) reads as an empty set and
	// keeps its `anilistid`: proving it a site card would need exactly the
	// per-record SCAN this sweep refuses, and a stamp left in place is the
	// safe direction.
	siteCard := make(map[string]bool, len(probe))
	for start := 0; start < len(probe); start += mgetBatch {
		end := start + mgetBatch
		if end > len(probe) {
			end = len(probe)
		}
		pipe := c.client.Pipeline()
		cmds := make([]*redis.StringSliceCmd, 0, end-start)
		for _, h := range probe[start:end] {
			cmds = append(cmds, pipe.SMembers(ctx, c.buildFieldsKey(h)))
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return nil, fmt.Errorf("smembers fields: %w", err)
		}
		for k, cmd := range cmds {
			fields, err := cmd.Result()
			if err != nil && err != redis.Nil {
				return nil, fmt.Errorf("smembers fields %s: %w", probe[start+k], err)
			}
			siteCard[probe[start+k]] = isSiteCard(fields)
		}
	}

	plans := make([]literatureEchoPlan, 0, len(cands))
	for _, cand := range cands {
		var fields []string
		if cand.hasAnilist && siteCard[cand.hashID] {
			fields = append(fields, "anilistid")
		}
		fields = append(fields, cand.echoes...)
		if len(fields) > 0 {
			plans = append(plans, literatureEchoPlan{hashID: cand.hashID, fields: fields})
		}
	}
	return plans, nil
}

// SweepLiteratureEchoesWithTimeout is the convenience wrapper used by the API
// handler. Generous timeout: the read phase is batched, but the write phase is
// one round-trip per deleted field.
func (c *Client) SweepLiteratureEchoesWithTimeout(apply bool) (LiteratureEchoSweep, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return c.SweepLiteratureEchoes(ctx, apply)
}
