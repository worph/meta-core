package schema

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metazla/meta-core/internal/events"
	"github.com/redis/go-redis/v9"
)

var langCodeRegex = regexp.MustCompile(`^[a-z]{2,3}$`)

// fieldState is the per-field accumulator. perKey lets us undo a previous
// contribution when the same Redis key is re-SET or DELed.
type fieldState struct {
	counts         map[string]int
	primitives     map[Primitive]int
	hintCounts     map[Hint]int
	typedCount     int
	undefinedCount int
	languageKeyed  bool
	perKey         map[string]labelPair
}

type labelPair struct {
	prim Primitive
	hint Hint
}

func newFieldState() *fieldState {
	return &fieldState{
		counts:     make(map[string]int),
		primitives: make(map[Primitive]int),
		hintCounts: make(map[Hint]int),
		perKey:     make(map[string]labelPair),
	}
}

// Indexer maintains a live field-schema by consuming meta:events.
type Indexer struct {
	client    *redis.Client
	keyPrefix string

	mu     sync.RWMutex
	fields map[string]*fieldState

	processed uint64 // total events processed since start

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	runMu   sync.Mutex
	running bool
}

// NewIndexer creates an Indexer. keyPrefix should match storage.Client.GetPrefix()
// so the indexer can build absolute Redis keys for GET calls.
func NewIndexer(client *redis.Client, keyPrefix string) *Indexer {
	ctx, cancel := context.WithCancel(context.Background())
	return &Indexer{
		client:    client,
		keyPrefix: keyPrefix,
		fields:    make(map[string]*fieldState),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins consuming the meta:events stream from the earliest available ID.
func (i *Indexer) Start() error {
	i.runMu.Lock()
	if i.running {
		i.runMu.Unlock()
		return nil
	}
	i.running = true
	i.runMu.Unlock()

	i.wg.Add(1)
	go i.consumeLoop()

	log.Println("[SchemaIndexer] Started - consuming meta:events stream")
	return nil
}

// Stop terminates the consumer goroutine.
func (i *Indexer) Stop() error {
	i.runMu.Lock()
	if !i.running {
		i.runMu.Unlock()
		return nil
	}
	i.running = false
	i.runMu.Unlock()

	i.cancel()
	i.wg.Wait()
	log.Println("[SchemaIndexer] Stopped")
	return nil
}

// ProcessedCount returns the total number of stream events processed.
// Used by WaitForDrain to detect when a rescan flood has been absorbed.
func (i *Indexer) ProcessedCount() uint64 {
	return atomic.LoadUint64(&i.processed)
}

// WaitForDrain blocks until ProcessedCount has been stable for `stableFor` or
// the timeout elapses. Suitable for use right after RepublishMetadata.
func (i *Indexer) WaitForDrain(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	last := i.ProcessedCount()
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		now := i.ProcessedCount()
		if now != last {
			last = now
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= 300*time.Millisecond {
			return
		}
	}
}

func (i *Indexer) consumeLoop() {
	defer i.wg.Done()
	lastID := "0"
	for {
		select {
		case <-i.ctx.Done():
			return
		default:
		}

		res, err := i.client.XRead(i.ctx, &redis.XReadArgs{
			Streams: []string{events.MetaEventsStream, lastID},
			Block:   2 * time.Second,
			Count:   500,
		}).Result()

		if err != nil {
			if err == redis.Nil || i.ctx.Err() != nil {
				continue
			}
			log.Printf("[SchemaIndexer] XRead error: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, stream := range res {
			for _, msg := range stream.Messages {
				lastID = msg.ID
				i.processEvent(msg.Values)
				atomic.AddUint64(&i.processed, 1)
			}
		}
	}
}

func (i *Indexer) processEvent(values map[string]any) {
	op, _ := values["type"].(string)
	key, _ := values["key"].(string)
	if key == "" {
		return
	}
	fieldPath, keyHint, ok := parseFieldPath(key)
	if !ok {
		return
	}

	switch op {
	case "set":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		val, err := i.client.Get(ctx, i.keyPrefix+key).Result()
		cancel()
		exists := err != redis.Nil
		if err != nil && err != redis.Nil {
			// Treat transient errors as "value disappeared" — nothing to record.
			return
		}
		prim, hint := ClassifyValue(val, exists)
		i.upsert(fieldPath, key, keyHint, prim, hint)
	case "del", "expire":
		i.remove(fieldPath, key)
	}
}

// parseFieldPath turns "file:<rootID>/<propertyPath>" into the logical field
// path used as the map key. rootID is opaque to the indexer — currently a
// UUIDv7, historically a midhash256:abc token; both shapes parse identically
// because the split is on the first '/' after "file:".
//
// When the last segment of propertyPath matches ^[a-z]{2,3}$ it is treated as
// a language code: the field name collapses to the prefix and key_hint is set.
func parseFieldPath(key string) (string, KeyHint, bool) {
	if !strings.HasPrefix(key, "file:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, "file:")
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", "", false
	}
	property := rest[slash+1:]
	if property == "" {
		return "", "", false
	}
	if last := strings.LastIndex(property, "/"); last >= 0 {
		tail := property[last+1:]
		if langCodeRegex.MatchString(tail) {
			return property[:last], KeyHintLanguageCode, true
		}
	}
	return property, "", true
}

func (i *Indexer) upsert(fieldPath, redisKey string, keyHint KeyHint, prim Primitive, hint Hint) {
	i.mu.Lock()
	defer i.mu.Unlock()

	fs, ok := i.fields[fieldPath]
	if !ok {
		fs = newFieldState()
		i.fields[fieldPath] = fs
	}
	if keyHint != "" {
		fs.languageKeyed = true
	}

	if old, ok := fs.perKey[redisKey]; ok {
		decContribution(fs, old)
	}

	fs.perKey[redisKey] = labelPair{prim: prim, hint: hint}
	incContribution(fs, labelPair{prim: prim, hint: hint})
}

func (i *Indexer) remove(fieldPath, redisKey string) {
	i.mu.Lock()
	defer i.mu.Unlock()

	fs, ok := i.fields[fieldPath]
	if !ok {
		return
	}
	old, ok := fs.perKey[redisKey]
	if !ok {
		return
	}
	delete(fs.perKey, redisKey)
	decContribution(fs, old)
	if len(fs.perKey) == 0 {
		delete(i.fields, fieldPath)
	}
}

func incContribution(fs *fieldState, lp labelPair) {
	label := breakdownLabel(lp.prim, lp.hint)
	fs.counts[label]++
	if lp.prim == PrimUndefined {
		fs.undefinedCount++
		return
	}
	fs.typedCount++
	fs.primitives[lp.prim]++
	if lp.hint != "" {
		fs.hintCounts[lp.hint]++
	}
}

func decContribution(fs *fieldState, lp labelPair) {
	label := breakdownLabel(lp.prim, lp.hint)
	fs.counts[label]--
	if fs.counts[label] <= 0 {
		delete(fs.counts, label)
	}
	if lp.prim == PrimUndefined {
		fs.undefinedCount--
		return
	}
	fs.typedCount--
	fs.primitives[lp.prim]--
	if fs.primitives[lp.prim] <= 0 {
		delete(fs.primitives, lp.prim)
	}
	if lp.hint != "" {
		fs.hintCounts[lp.hint]--
		if fs.hintCounts[lp.hint] <= 0 {
			delete(fs.hintCounts, lp.hint)
		}
	}
}

// Snapshot returns the current schema. Safe to call concurrently with consumption.
func (i *Indexer) Snapshot() *SchemaResponse {
	i.mu.RLock()
	defer i.mu.RUnlock()

	out := &SchemaResponse{
		Fields:      make(map[string]*FieldSchema, len(i.fields)),
		GeneratedAt: time.Now().UTC(),
		Source:      "live",
	}
	for fp, fs := range i.fields {
		out.Fields[fp] = computeFieldSchema(fs)
	}
	return out
}

func computeFieldSchema(fs *fieldState) *FieldSchema {
	out := &FieldSchema{
		Breakdown: make(map[string]int, len(fs.counts)),
	}
	for k, v := range fs.counts {
		out.Breakdown[k] = v
	}

	nPrim := len(fs.primitives)
	hasUndef := fs.undefinedCount > 0

	switch {
	case nPrim == 0 && hasUndef:
		out.Type = PrimUndefined
	case nPrim == 1 && !hasUndef:
		for p := range fs.primitives {
			out.Type = p
		}
	default:
		out.Type = PrimMixed
	}

	// Strict hint promotion: exactly one hint observed, and it covers every
	// typed (non-undefined) value. Hints are never set on mixed fields.
	if out.Type != PrimMixed && len(fs.hintCounts) == 1 && fs.typedCount > 0 {
		for h, c := range fs.hintCounts {
			if c == fs.typedCount {
				out.Hint = h
			}
		}
	}

	if fs.languageKeyed {
		out.KeyHint = KeyHintLanguageCode
	}

	return out
}
