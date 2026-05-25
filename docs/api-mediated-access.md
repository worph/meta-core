# API-Mediated Access (Redis Lockdown)

## Status

**Landed** — all four PRs (A/B/C/D) deployed and verified end-to-end on the
dev stack. Redis is unreachable from outside `metacore-app`; every metadata
read, write, and event flows through meta-core's HTTP+SSE surface. Builds
on [uuid-rooted-metadata.md](uuid-rooted-metadata.md), which assumes
meta-core is the single writer to the metadata keyspace — an assumption
that's now actually enforced rather than aspirational.

See "Implementation outcome" at the bottom for the delta between this doc's
plan and what shipped (notably the write-path CID resolution and the
dual-root migration sweep, which weren't in the original plan but turned
out to be load-bearing).

## Motivation

meta-core was designed as the sidecar that mediates every metadata read and
write — a single point that owns the Redis schema, enforces invariants
(reverse index, canonical_cid reconciliation, schema-version sentinel),
and presents a stable HTTP contract. Other services were meant to talk to
meta-core, not to Redis.

In practice, meta-sort opens its own Redis connection and writes
`file:<hashId>/<property>` keys with raw pipeline SETs
(`packages/meta-sort/packages/meta-sort-core/src/kv/RedisClient.ts:269-296`).
It picked up this shortcut early, before meta-core had a complete write
API, and the shortcut stuck.

This bypass is a real problem in three ways:

1. **Schema invariants leak.** When meta-core landed the UUID-rooted schema
   ([uuid-rooted-metadata.md](uuid-rooted-metadata.md)), meta-sort kept
   writing midhash-rooted entries. The result was *dual roots per file* —
   one set of keys at the watcher's UUID, another at meta-sort's midhash.
   `file:__index__` doubled in size. The bug was invisible to meta-sort
   (its writes succeeded) but broke the reverse-index resolution
   downstream services rely on.

2. **Schema evolution is impossible.** Changing the Redis layout requires
   coordinating across every service that touches it. The UUID-root
   migration would have been a single-PR change if writes already routed
   through the API.

3. **Self-hosted distributed deployment widens the threat model.** Peers
   are run by different operators. Plugin authors may ship their own
   services. We can't expect every binary in the network to honour
   undocumented conventions. A buggy or careless service that connects
   to Redis and writes the wrong key shape can corrupt the schema for
   everybody on that node.

The third point is the one that flipped the design from "soft lock by
convention" to "hard lock by network topology." We're not protecting
against malicious services — that's an out-of-scope threat. We're
protecting against *accidental* corruption from honest mistakes in code
we don't fully control.

## The architecture in one picture

```
                meta-core (only Redis writer + reader)
                ┌──────────────────────────────────────┐
                │  HTTP API surface:                   │
                │    POST/PUT/PATCH /api/metadata/*    │  ◄── writes
                │    GET            /api/meta/{cid}    │  ◄── reads (by CID)
                │    GET            /api/metadata/*    │  ◄── reads (by id)
                │    GET            /api/file/{cid}    │  ◄── content
                │    GET            /api/events/files  │  ◄── SSE stream
                │    GET            /api/events/meta   │  ◄── SSE stream
                │     │                                │
                │     ▼                                │
                │  Redis  (file:*, cid:*, streams)     │
                │  ◄── container-local, no exposed     │
                │      port, no docker-network alias   │
                └──────────────────────────────────────┘
                          ▲             ▲
                          │ HTTP        │ HTTP SSE
                          │             │
              ┌───────────┴────┐ ┌──────┴─────────┐
              │ meta-sort      │ │ meta-fuse      │
              │ meta-stremio   │ │ meta-share     │
              │ meta-dup       │ │ (any future)   │
              └────────────────┘ └────────────────┘
```

**The single-writer rule.** meta-core has exclusive write authority over
the `file:*`, `cid:*`, `file:__index__`, and `meta-core:*` keyspaces. No
other service can write — not because of an honoured convention, but
because they can't reach Redis at all.

**Stream reads also mediated.** Events go out through SSE endpoints on
meta-core's HTTP surface. Consumers don't see the underlying Redis
Streams. This is the Kubernetes / Consul pattern (see "Industry context"
below). It costs an extra HTTP hop versus the current direct XREAD; the
benefit is that the storage layer is free to evolve (or be replaced)
without breaking every consumer.

**Redis is private.** No host port mapping, no docker-network alias, no
URL in `kv-leader.info`. Discovery returns only meta-core's HTTP base.

## Eventing — HTTP Server-Sent Events

SSE is the chosen transport for cross-service event consumption. The
mapping from Redis Streams onto SSE is direct enough that no eventing
feature we currently use is lost.

### Feature mapping (what we keep, how we keep it)

| Redis Streams | SSE equivalent |
|---|---|
| Fan-out to N consumers | meta-core holds N SSE connections, broadcasts events to each |
| Replay from a cursor | `Last-Event-ID` header on reconnect — built into the SSE spec |
| Retention / MAXLEN | Underlying Redis Stream still does the trimming; SSE is a thin shim |
| Backpressure on slow consumers | meta-core's SSE handler blocks on the TCP write; events stay in Redis until trimmed |
| Multiple streams (file:events, meta:events) | Separate endpoints: `/api/events/files`, `/api/events/meta` |
| At-least-once via XACK | Implicit — client persists last-received `id`, sends it on reconnect (see "Cursor semantics and consumer-group migration" below) |

The one Streams feature we don't get is **consumer groups for
horizontally-scaled replicas of the same logical consumer.** We don't use
this today and don't plan to — meta-sort, meta-fuse, etc. each run one
instance. If horizontal scale ever happens, it'll be by sharding hash-IDs
across replicas, not by splitting an event stream. SSE stays adequate.

### Cursor semantics and consumer-group migration

Today three consumers use `XREADGROUP` against named consumer groups, not
bare `XREAD`:

- meta-sort: group `meta-sort-processor`
  (`packages/meta-sort/.../events/FileEventConsumer.ts:23,66-74`)
- meta-dup: group on `file:events`
  (`packages/meta-dup/.../MetaEventConsumer.ts:102-104`)
- meta-fuse: dual path — `XREADGROUP` on one consumer, plain `XREAD` on
  another (`packages/meta-fuse/.../kv/RedisClient.ts:826,907`)

`XREADGROUP` keeps server-side state per group: a Pending Entries List
(PEL) and a `last-delivered-id` cursor. The load-bearing use today is
meta-sort's `processPendingEntries(idleMs=30000)`, which exists to claim
work from a crashed consumer's PEL on restart.

SSE has no equivalent server-side state. After the migration the contract
shifts to: **each consumer persists its own `Last-Event-ID` and resumes
from it on reconnect.** Implications:

- PEL-based crash recovery is gone. A crashed consumer that hadn't
  persisted its cursor since entry X resumes from X (its last *durably
  persisted* cursor), not from "whatever was in flight." Acceptable for
  every current consumer — none run multiple instances per group — but
  call out in the PR C migration of meta-sort that the recovery primitive
  is changing.
- Each consumer must own its cursor persistence. Cheapest place is the
  consumer's own state (a file, a Redis key written under its own
  prefix, an in-memory variable if the consumer is OK with at-most-once
  on its own crash).
- No server-side "ACK" call. The cursor *is* the ack — when the consumer
  successfully writes Last-Event-ID = N to its own state, entries ≤ N
  are considered handled. The SSE handler does nothing.

### Endpoint spec

```
GET /api/events/files
  Request headers:
    Last-Event-ID: <stream-entry-id>   (optional; resume from this cursor)
    Accept: text/event-stream
  Response:
    Content-Type: text/event-stream
    Cache-Control: no-cache
    Connection: keep-alive
    X-Accel-Buffering: no    (disable nginx response buffering)
```

Same shape for `/api/events/meta`.

### Event format

One SSE event per Redis Stream entry. Format:

```
id: 1747999999999-0
event: add
data: {"path":"/watch/foo.mkv","size":4500000000,"midhash256":"bafk…","timestamp":1747999999999}

id: 1748000000123-0
event: change
data: {"path":"/watch/foo.mkv","size":4500000000,"midhash256":"bafk…","timestamp":1748000000123}
```

- `id` — **opaque** cursor. Today it's the Redis Stream entry ID
  (`<ms>-<seq>`); consumers MUST treat it as a black-box string they
  echo back via `Last-Event-ID`. This contract lets meta-core swap the
  backing store later without breaking every consumer's cursor handling.
- `event` — the `type` field from the underlying entry (`add` | `change`
  | `delete` | `rename` | `reset` for `file:events`; `set` | `del` |
  `expire` for `meta:events`).
- `data` — JSON-encoded rest of the entry. Keys match the existing stream
  payload exactly so downstream consumers don't change their parser.

### Backing-store retention and lifecycle

**Contract: the SSE wire is a faithful mirror of the underlying Redis
Streams.** Consumers rely on `Last-Event-ID` resumption working across
*every* kind of meta-core hiccup, including a restart — that's the entire
point of having a cursor protocol. The backing store must therefore
preserve entries across meta-core process restart and bound its growth
explicitly, not implicitly.

Today's behaviour is wrong on both counts:

| Stream | Restart behaviour | Retention |
|---|---|---|
| `meta:events` | **Cleared on every MetaPublisher.Start** (`events/meta_publisher.go:50-58`) | No `MAXLEN` (unbounded) |
| `file:events` | Persists across restart | No `MAXLEN` (unbounded), cleared only on deliberate reset (`watcher/dispatcher.go:82-95`) |

The `meta:events` clear-on-restart is the load-bearing problem. With it
in place, every meta-core restart silently invalidates every consumer's
persisted cursor — the SSE `gap` event would fire on the first reconnect
after every restart, and the consumer would have to re-bootstrap. That
turns "restart meta-core to apply a config change" into "every downstream
service does a full re-sync," which is exactly the regression SSE is
supposed to prevent.

PR A must fix both streams:

1. **Remove the unconditional `Del(MetaEventsStream)` in
   `MetaPublisher.Start`.** Entries persist across restart. The republish
   path (`RepublishAllMetadata`) stays as a one-shot bootstrap for
   missing entries, but it does not pre-truncate the stream.
2. **Add `MAXLEN ~ N` to every `XAdd` on both streams** so growth is
   bounded. Approximate trim (`~`) is cheap. Default `N = 100_000` per
   stream is fine for the foreseeable workload (a few KB per entry,
   bounded total ≈ a few hundred MB) and easy to tune via config.
3. **Keep the deliberate `EmitReset → ClearStream` path on
   `file:events`.** That clear is a feature: a reset is the consumer
   contract for "throw out everything you knew, re-bootstrap." Consumers
   detect it through the `event: reset` payload that's already emitted
   as the first entry after the clear (`dispatcher.go:99-`). The SSE
   `gap` event handles the race where a consumer's cursor was trimmed
   *between* the clear and its reconnect.

After PR A, the cursor contract is **"durable across meta-core restart
and bounded by `MAXLEN ~ N`"** — matching what consumers already assume
when they persist a Last-Event-ID.

### Heartbeats

Server writes an SSE comment line (`:keep-alive\n\n`) every 30 seconds
when no events are flowing. Keeps reverse proxies and load balancers
from killing the connection as idle. The client ignores comment lines
per the SSE spec.

### Reconnect semantics

Per the SSE spec the client (or `EventSource`) auto-reconnects after a
network error, sending `Last-Event-ID`. The server resumes from the next
entry after that ID by translating to Redis Stream `XREAD … STREAMS file:events <id>`.

**Gap handling.** If the requested ID has been trimmed out of the Redis
Stream by retention, the server emits one synthetic event before resuming
from the oldest available entry:

```
event: gap
data: {"requested":"<id>","resumeFrom":"<oldest-id>","reason":"retention"}
```

Consumers that care about gap-free delivery (currently none — the
schema indexer tolerates gaps; ingest tasks rebuild from scratch) can
react. Consumers that don't care ignore it.

### Error handling

- **Redis unreachable on connect**: 503 with `Retry-After: 5`.
- **Redis fails mid-stream**: close the SSE connection. Client reconnects
  with `Last-Event-ID`; the cycle resumes when Redis recovers.
- **Client disconnects**: handler goroutine exits cleanly; no per-client
  state on meta-core.

### Auth

`/api/events/*` bypasses the hash-lock perimeter, same pattern as
`/api/meta/{cid}` and `/api/file/{cid}`. Event streams are internal
infrastructure and need to be reachable by peer services without going
through Authelia.

**Bypass ≠ public.** "Hash-lock bypass" means the OIDC sidecar doesn't
gate the route; it does *not* automatically mean Caddy proxies it from
the public hostname. The two existing precedents differ on this:
`/api/file/{cid}` is publicly reachable (peer-to-peer content fetch is
its job), `/api/meta/{cid}` likewise. For the SSE endpoints, the choice
is deliberate:

- If kept inside-only (no Caddy upstream for `/api/events/files` and
  `/api/events/meta`), only sibling services on the docker network can
  subscribe. This matches the rest of the "Redis is private" model and
  is the recommended default.
- If exposed publicly, any browser can open an `EventSource` against
  the stream without authentication — i.e. anyone can watch every file
  event in the library. Only do this if there's a concrete consumer
  that needs it and the visibility is acceptable.

PR A's Caddy change (see PR A scope, item 4) makes this an explicit
choice rather than a default behaviour. The recommendation is
inside-only until a concrete external consumer appears.

### Implementation footprint

The SSE handler itself is ~80 lines of Go in meta-core: one handler per
stream, each running an `XREAD … BLOCK 30s` loop and writing
SSE-formatted lines to the response. Go's `net/http` natively supports
streaming responses with `http.Flusher.Flush()`. Standard SSE clients
exist for every language we use: Node `eventsource`, Rust
`eventsource-client` (or `reqwest` + `tokio_util::codec`), Python
`sseclient`, Go `r3labs/sse`.

PR A as a whole is larger than the handler — see the PR A scope below
for the full list (backing-store fixes, Caddy wiring, poll deprecation)
that has to land alongside the handler for the cursor contract to hold.

## Eventing — considered alternatives

Recorded for posterity. None of these are being implemented; if any are
ever revisited it should be a deliberate reopen, not a drift.

- **Long-polling.** Strictly worse than SSE — connection churn on every
  event, latency floor at the poll interval, no built-in resumption.
  Rejected.
- **Webhooks (meta-core POSTs to subscribers).** Symmetric coupling:
  every consumer would need to run an HTTP server reachable from
  meta-core. Out-of-order delivery, retry, dead-letter all need
  reinventing. Right for cross-organization integrations, wrong for
  in-cluster eventing. Rejected.
- **External broker (Kafka / NATS / RabbitMQ).** Industrial-grade
  pub/sub. New dependency, new operational surface, overkill for the
  scale (single-host dev, ≤20-peer POC). Revisit if MetaMesh ever needs
  cross-WAN federation at high throughput. Rejected for now.
- **Two Redises (writes Redis private, events Redis exposed).** Strongest
  separation. Doubles operational complexity for no benefit over the
  SSE design — once events go through HTTP, the events-Redis isn't
  exposed either. Rejected.
- **Redis ACLs (Option B from earlier brainstorm).** Allows direct Redis
  access from clients while restricting commands by user. Adds
  credential plumbing and ACL config to operate; lets a buggy service
  still connect, just with limited verbs. The SSE design closes the
  network entirely, which is strictly stronger. Rejected — but the
  equivalent escape hatch (open Redis to other services again) stays
  available if we ever need it.

## Industry context — where this lands

The pattern we're adopting is the **Kubernetes / etcd** pattern, and
that's the most informative comparison.

Kubernetes is structurally identical to MetaMesh:
- A single source-of-truth store (etcd) holds all cluster state.
- A single service (kube-apiserver) is the sole writer to etcd.
- Many polyglot clients (kubectl, controllers, schedulers, kubelets,
  operators) read and react to changes.
- Clients subscribe to *watch streams* for events.

What Kubernetes does, point-for-point:
- **etcd is never reached by clients.** It runs on a separate port,
  often a separate network. kube-apiserver is the only thing that holds
  etcd credentials. The lock is by network topology, not authentication.
- **Watch is also mediated.** kube-apiserver exposes `/watch` as an HTTP
  streaming endpoint (very similar to SSE in shape). Clients don't watch
  etcd directly — even though etcd has its own native watch primitive.
- **Storage portability falls out for free.** etcd could be swapped for
  another store without any client change. We don't plan to swap Redis,
  but the optionality is real and free.

Other systems with the same shape:
- **HashiCorp Consul.** Clients talk to a local agent over HTTP. The
  agent talks to the cluster. KV writes and watches both flow through
  the agent. Direct raft-store access is impossible.
- **Apache ZooKeeper.** Clients use the ZooKeeper client library which
  speaks the ZooKeeper wire protocol; the data store is private.
- **Strapi / Sanity / Contentful** (headless CMS as analog). All
  consumers — frontends, backend services, build pipelines — go through
  the CMS HTTP API. Direct Postgres or content-store access is impossible
  by design.

Where MetaMesh sat *before*: closer to the bad-monolith pattern
(everyone connects directly to the shared Redis). Where it sits *after*
this design: aligned with the canonical microservices pattern, and
specifically with the Kubernetes flavour where streams are mediated too.

### Advantages we're picking up

- **Schema evolution.** One service can change its data layout without
  coordinating releases across every other service. The UUID-root
  migration is the cautionary tale of what happens without this.
- **Validation choke point.** Every write passes through one code path
  that can enforce invariants (reverse index, canonical_cid, type
  constraints, schema version).
- **Observability.** Audit logs, request metrics, slow-query detection
  all live at one layer.
- **Threat-model fit.** Untrusted-service-in-the-network is now in
  scope; network isolation makes the concern moot.
- **Storage portability.** Optionality only, but real.

### Drawbacks (acknowledged)

- **Latency.** One extra hop per write (TCP + HTTP parse + handler).
  Local-host overhead is sub-millisecond; mitigations are batched writes
  (PATCH the whole metadata blob per plugin completion, not per field)
  and Keep-Alive / HTTP/2 connections.
- **SSE-handler plumbing.** ~80 lines per stream endpoint plus per-client
  goroutines. Trivial at our scale, real at Kubernetes scale (which is
  why they invested in chunked watch with bookmarks, resource versions,
  etc.). We can defer the same optimisations forever.
- **API surface size.** Every operation needs a corresponding endpoint.
  Most of it already exists; one new SSE endpoint per stream is the
  only new surface area introduced here.

## Enforcement — what "lock" actually means

Three layers, in order of effectiveness:

1. **Network isolation (hard).** Redis is on a container-private port.
   No host port mapping. No docker-network alias. `kv-leader.info` does
   not publish a Redis URL. Any service that wants to talk to Redis
   needs container privileges, and at that point the lock isn't the
   concern — operating-system isolation is. This is the actual lock.

2. **Documentation (convention).** This file, plus a one-line note in
   `CLAUDE.md` and in each consumer service's `CLAUDE.md`: "All metadata
   I/O routes through meta-core's HTTP API. Redis is private to
   metacore-app." Engineers reading the codebase find the rule on first
   contact. This catches the case of a future meta-core PR that
   accidentally exposes Redis again.

3. **Code review (convention).** PRs that re-expose Redis (a port
   mapping, an alias, a credential leak via leader-info) get rejected
   with a pointer to this doc.

No runtime canary or lint is proposed — the network isolation makes
violations physically impossible from outside, and inside meta-core the
small file count makes review sufficient. Layer 1 does the heavy
lifting.

## Migration plan

Four PRs, additive, ordered so the system stays working throughout.
Closing the network access (PR D) is the moment the lock becomes a
lock; everything before it is opt-in migration that consumers can adopt
at their own pace.

**All four PRs have landed.** What follows is the original plan as
written; see "Implementation outcome" at the bottom for the delta
between plan and ship.

### PR A — SSE endpoints in meta-core

Adds `GET /api/events/files` and `GET /api/events/meta`. Implements the
`Last-Event-ID` semantics, gap handling, heartbeats, and the per-stream
XREAD loop. Redis Streams stay reachable directly; consumers can
migrate one at a time.

Scope, in PR-order:

1. Backing-store fixes (see "Backing-store retention and lifecycle"
   above) — remove the `meta:events` clear-on-restart, add `MAXLEN ~ N`
   to every `XAdd`. These are prerequisites for the SSE cursor contract
   to hold; without them PR A is not deliverable.
2. SSE handlers for the two streams.
3. Deprecate `GET /api/events/poll` (`watcher/handlers.go:29`). It
   exists today as a long-poll variant over the same `file:events`
   stream and is described in code as "always empty in practice"
   (`watcher/handlers.go:55-58`). After SSE lands it's redundant — keep
   it responding 200 for one release with a `Deprecation` header, then
   remove. The `/api/scan/*` deprecation pattern in the same file is
   the template.
4. Caddy perimeter wiring (see "Auth" subsection above) — explicit
   allow-list for `/api/events/files` and `/api/events/meta` matching
   whatever the perimeter policy decides. Either inside-only via the
   docker network, or public via Caddy with deliberate authorisation.
   Do not let the routes default-leak past the hash-lock.

Roughly 200 lines of Go + ~50 lines of test (covering reconnect from a
cursor, gap emission when the cursor is trimmed, heartbeat cadence,
durability across a meta-core restart).

### PR B — meta-sort writes via HTTP

Swap `RedisClient` for an `HttpKVClient` implementation of the existing
`IKVClient` interface in `packages/meta-sort-core`. All writes
(`setMetadataFlat`, `setMetadataProperty`, batch updates) become HTTP
calls against the **`/meta/{hash}` family** — the canonical write
surface for service-to-service traffic. Per-plugin write coalescing
uses `PATCH /meta/{hash}` (already calls `MergeMetadataFlat`
server-side with the partial-update semantics we need; see
`handlers.go:533-566`).

Reads from Redis stay direct in this PR (no consumer migration yet);
only writes move. Streams stay on direct XREAD.

**Pre-audit (already done):** meta-sort's production `kvClient.*`
surface is `setMetadataFlat` / `deleteMetadataFlat` / `getMetadataFlat`
/ `getMetadata` / `getAllHashIds` / `health` plus the `file:events`
stream consumer. No orphan keyspaces, no generic `kv.set(arbitrary_key)`
callers — the HTTP migration covers the entire write path with no
side bets.

### PR C — Stream consumers migrate to SSE

meta-sort's event consumer (`FileEventConsumer.ts`), meta-fuse's stream
reader, meta-stremio's event hook, meta-dup's monitor, meta-share's
ingest task — each swaps its `XREADGROUP` / `XREAD` loop for an SSE
client. Per-language standard libraries make this small (one HTTP
request, one event-handler closure).

In the same PR, each consumer's `LeaderClient` (TypeScript x2, Python,
plus any new Rust binding for meta-share if added) drops its
*requirement* on the `redisUrl` field — i.e. it stops opening a Redis
connection. It can still tolerate the field being present (PR D removes
it from the publisher side). This ordering matters: PR D's loud-failure
guard cannot ship until every consumer has been demonstrated to boot
without `redisUrl`.

The schema indexer is internal to meta-core and unaffected.

### PR D — Close Redis network access

PR D must land **after** every consumer in PR C has stopped requiring
`redisUrl` (verify by booting each service against a meta-core build
that already omits the field — staging environment or feature-flagged
release). Until that's true, this PR will deadlock the boot sequence.

Then, in this order:

1. Update consumer-side discovery (`LeaderClient` in each service) to
   *log loudly* if it sees a `redisUrl` field — guards against
   accidental rollback. (Loud log, not hard failure, while the field
   is still being written — flip to hard failure once step 4 lands.)
2. Stop publishing `redisUrl` from `/urls` and from `kv-leader.info`.
3. Remove the `meta-core` docker-network alias on the metacore-app
   service (other services no longer need DNS for the Redis port).
4. Remove `6380:6379` host port mapping in `dev/docker-compose.yml`.
5. Flip the LeaderClient guard from "warn" to "raise" — any future
   consumer that tries to require the field fails loudly at boot.

After PR D, attempting to connect to Redis from anywhere except
metacore-app produces a connection refused. The lock is real.

### Out of scope for this work

- **Redis ACLs / proxy.** Network isolation makes these unnecessary.
  Available as an escape hatch if the threat model ever changes.
- **Replacing Redis Streams under the hood.** SSE is the externalised
  contract; the backing store is an implementation detail meta-core can
  change later.
- **External brokers.** Out of scope absent a concrete driver
  (cross-WAN federation at high throughput).

## Decisions recorded

| Question | Decision | Notes |
|---|---|---|
| Writes from non-meta-core services? | Forbidden by network topology | Redis unreachable; not a convention |
| Reads from `file:*` direct? | Forbidden by network topology | Same lock, same reason |
| Event streams direct? | No — mediated through SSE | Kubernetes-style. Costs one HTTP hop; buys schema evolution + threat-model fit |
| Enforcement mechanism? | Network isolation (primary) + docs + code review (secondary) | No ACLs, no proxy, no lint |
| Backing store for streams? | Redis Streams (unchanged) | SSE wraps it; could be swapped later without consumer impact |
| Consumer groups / horizontal scale? | Not supported via SSE | Not needed today; revisit if scale ever requires it |
| Storage portability? | Not a goal but a free side-effect | Will not be exercised in alpha |
| **Canonical write API path?** | **`/meta/{hash}` family** | Has GET/PUT/**PATCH**/DELETE plus `/_add/{key}` and per-property `{key:.*}` operations. `PATCH` calls `MergeMetadataFlat` — matches the partial-update semantics PR B needs. `/api/metadata/{hashId}` stays as the editor-internal surface (no PATCH, no per-property routes) but is not the migration target. |
| **SSE event-id contract?** | **Opaque to consumers** | Today it's the Redis Stream entry ID (`<ms>-<seq>`); consumers MUST treat as opaque so the backing store can be swapped without breaking the wire contract. Documented in the SSE endpoint comment and in this doc. |
| **Typed error envelope?** | **`{error, message, retryable}` with stable `error` slug** | See "Error envelope" subsection below. |
| **Plugin direct-Redis risk?** | **Confirmed: no plugin uses Redis directly** | Grep across `packages/plugins/` for `ioredis` / `redis.createClient` / `new Redis()` returned empty. Plugins POST results back to meta-sort via `/api/plugins/callback`; meta-sort then writes. PR B picks up the plugin write path transparently. |
| **meta-sort non-`file:*` writes?** | **Confirmed: none in production** | Full audit of `kvClient.*` callers in `packages/meta-sort/packages/meta-sort-core/src/` shows only: `setMetadataFlat`, `deleteMetadataFlat`, `getMetadataFlat`, `getMetadata`, `getAllHashIds`, `health`, plus the `file:events` stream consumer (`initStreamConsumer` / `startStreamConsumer` / `processPendingEntries`). The generic `kv.set(key, value)` / `kv.setProperty(key, value)` methods on the interface exist for plugin sandboxes and tests but are not invoked by production code with non-`file:*` keys. PR B + PR C cover the full meta-sort surface. |

## Error envelope

Standard shape for all 4xx / 5xx responses from the metadata API:

```json
{
  "error":     "alias_collision",
  "message":   "CID midhash256:bafk… already resolves to UUID 01JKR…",
  "retryable": false
}
```

Stable `error` slugs the client should switch on:

| Slug | HTTP status | Retryable | Meaning |
|---|---|---|---|
| `alias_collision` | 409 | no | A different UUID already owns the CID the caller is trying to register. Caller bug; escalate. |
| `unknown_root` | 404 | no | The hashId / UUID doesn't exist. Caller probably has a stale reference. |
| `unknown_cid` | 404 | no | The CID isn't in the reverse index. No retry helps. |
| `schema_violation` | 400 | no | Body shape rejected (missing required field, invalid value). Caller bug. |
| `storage_unavailable` | 503 | yes | Redis transient. Caller should retry with backoff. |
| `internal` | 500 | yes (cautiously) | Unhandled server-side error. Retry once; escalate if persistent. |

**Implemented.** Every 4xx/5xx response across `internal/api`,
`internal/mounts`, `internal/watcher`, and `internal/watchers` now
emits the typed envelope. The slug-mapping helper is duplicated across
those packages (one ~15-line `slugForStatus` per package) to avoid an
import cycle on the api package; consolidating it into a shared
`internal/httperr` package is a worthwhile follow-up but not load-bearing.

## Remaining open questions

Mostly answered post-implementation:

- **Deprecation timeline for `/api/metadata/{hashId}`.** Still open. The
  editor's React UI continues to call it; the surface itself was untouched
  by this work. Worth a follow-up to consolidate the editor onto
  `/meta/{hash}` and delete the parallel routes.

- **Initial bootstrap flood on `/api/events/files`.** Resolved in
  practice: meta-fuse uses `initialCursor: '0-0'` to deliberately
  replay every entry on each restart and the StreamingStateBuilder
  handles the resulting burst (~2.5k property events on a 50-file
  library) without issue. meta-sort persists its cursor so only sees
  the flood once per fresh deploy. meta-stremio filters to "interesting
  fields" before triggering cache invalidation, so the burst is mostly
  no-ops.

- **meta-dup SSE port.** Still pending — meta-dup is in no active
  compose file. When revived, copy meta-fuse's pattern wholesale
  (SSEEventClient + MetaCoreApiClient). Tracked separately.

## Non-goals

- **Authentication between internal services.** The hash-lock OIDC
  perimeter handles external clients; internal service-to-service
  trust is by network isolation (docker network for dev, equivalent
  isolation in prod deployments). Adding service-to-service auth is a
  separate concern.
- **Replacing the public HTTP API surface.** This proposal exercises
  what already exists; the only new endpoints are the two SSE streams.
- **Introducing new transport, broker, proxy, or sidecar.** Everything
  builds on what's deployed today.
- **Migrating meta-fuse / meta-stremio / meta-dup direct Redis reads
  via XSCAN/GET** (separate from event-stream reads). ~~Was a non-goal~~
  ended up being required for PR D's network closure to be safe.
  meta-fuse and meta-stremio migrated; meta-dup deferred (not deployed).
  See PR C / "Implementation outcome" for details.

## Implementation outcome

What actually shipped vs. what this doc planned.

### Delta against the original plan

- **Read-path migration for meta-fuse and meta-stremio is now in scope
  and done.** The original "out of scope" carve-out was wrong: closing
  the Redis network port doesn't just break stream consumers, it also
  breaks every `kvClient.get` / `redis_storage.get_all_videos` call.
  Both services grew their own `MetaCoreApiClient` (TS for meta-fuse,
  Python for meta-stremio) and now route reads through `/meta/{hash}`
  + `/meta` + `/api/file/{cid}/info`.
  - **meta-stremio's HTTP mode auto-recovers from transient meta-core
    outages.** `is_connected` re-probes meta-core's `/health` on every
    call (cheap; a stateless GET), so a metacore-app restart no longer
    latches the addon into a permanent disconnected state. The
    background reconnect thread is only needed for legacy Redis mode
    where the socket itself is sticky.
- **Hard `redisUrl` guard is gated by `ALLOW_LEGACY_REDIS_URL=1`.** All
  three consumer LeaderClients (TS x2 + Python) throw on seeing the
  field by default. Setting the env var downgrades to a warn — escape
  hatch for a deliberate temporary rollback to a meta-core that still
  publishes the field.
- **Write-path CID resolution** turned out to be a hard prerequisite
  for the lockdown to work in practice. Documented separately below.
- **PR D ordering matched the doc** (consumer migration first, then
  publisher stops emitting, then network closure). The `meta-core`
  docker-network alias and the `6380:6379` host port mapping are both
  removed; sibling services reach the HTTP/SSE/WebDAV surface via the
  canonical `metacore-app` hostname.
- **meta-dup is the one service that didn't migrate.** It's in no
  active compose file. The TS port is straightforward when needed: copy
  meta-fuse's pattern (SSEEventClient for streams, MetaCoreApiClient
  for reads).

### Write-path CID resolution (`storage.ResolveRoot`)

Not in the original plan but load-bearing.

**The problem.** meta-sort's PATCH-via-HTTP migration (PR B) routed
every write through `/meta/{hash}`. But `{hash}` is the midhash256 CID
that meta-sort received via file:events, not the watcher's minted UUID.
With no resolution step in the handler, every write went to
`file:<midhash>/*` — a parallel root the watcher's UUID never saw. The
auto-alias hook on `cid_midhash256=<value>` then OVERWROTE the
watcher's `cid:midhash256:<value>` → UUID alias to point at the
midhash itself, leaving the UUID stranded with only the watcher's
sparse fields (filePath, sizeByte, mtimeNano, duplicates) and the
midhash root carrying the rich plugin output. This is the exact
"dual roots per file" failure the doc's Motivation section listed as
the prior bug — it just moved from "convention violation" to "API
contract violation."

**The fix.** Three layers in `internal/storage/cid_resolution.go`:

1. **`ResolveRoot(hash)`** — at the entry of every `/meta/{hash}`
   read/write handler, look up `cid:midhash256:<hash>` in the reverse
   index. If found, use the UUID as the storage root. Falls through to
   the bare hash for direct-UUID writes / legacy entries.
2. **Self-pointing alias guard in `addAliasLocked`** — refuse to
   register `cid:<algo>:<v>` → `<v>`. The dual-root bug's chicken-and-
   egg is: meta-sort writes first, registers self-pointing alias,
   watcher then can't recover. Refusing the self-write keeps the
   watcher's legitimate alias intact.
3. **`MigrateDualRoots` sweep** (`POST /api/admin/migrate-dual-roots`).
   One-shot fixer for already-stranded entries: walks every UUID's
   `midhash256` field to build the orphan index, merges each stranded
   `file:<midhash>/*` into the matching UUID via `MergeMetadataFlat`,
   deletes the stranded root, and re-points the alias.

Verified on the dev stack: 48 stranded entries were reunited; the
editor now shows every file with its full plugin metadata under one
UUID; future writes stay unified.

### Typed error envelope

Done across all four handler packages (`api`, `mounts`, `watcher`,
`watchers`). Each has its own `writeError` returning
`{error: <slug>, message, retryable}`. The slug-mapping helper is
duplicated in each to avoid an import cycle on the `api` package.

### What's NOT done

- **meta-dup SSE + read migration.** Not deployed, no test harness.
  When revived, copy meta-fuse's pattern.
- **Editor `/api/metadata/{hashId}` consolidation.** The duplicate
  editor-internal API surface is untouched; could be retired in a
  follow-up by porting the editor UI to `/meta/{hash}`.
- **Consolidated `internal/httperr` package** for the typed envelope
  helpers (currently duplicated 4× to avoid the import cycle). Code
  hygiene; functionally a no-op.

### Verification done

- All four service dashboards load cleanly with no console errors.
- meta-sort SSE consumer + HTTP write path verified end-to-end by
  delete + inject + reprocess of Sintel.
- meta-fuse SSE consumer rebuilds VFS to 48 files on each boot.
- meta-stremio catalog API now surfaces the full library (47 videos,
  1 movie + 5 series + 46 episodes — was 0/0/0 before the Python
  read-path migration because LeaderStorage broke when it tried Redis).
- `redis-cli` from any container other than `metacore-app` produces a
  connection refused; `cid:midhash256:<H>` aliases all point to UUIDs
  (no self-pointing); `file:__index__` has only UUID-style hashIds.
- ALLOW_LEGACY_REDIS_URL=1 escape hatch present in all three consumer
  LeaderClients (TS x2 + Python).

### Known test-suite gaps (post-implementation bats run)

Running `docker exec meta-test-runner /app/test/test.sh` after the
lockdown shipped reveals a number of failing suites, almost all of
which are pre-existing test/environment drift unrelated to this work:

| Failure | Root cause | Relation to lockdown |
|---|---|---|
| meta-stremio (~35 tests), stremio-tmdb (6 tests) | Tests call `/manifest.json` directly; dev `.env` sets `STREMIO_HASH_API_SEED=dev-stremio-secret` which puts the addon behind a path-token prefix | Unrelated — pre-existing env mismatch |
| meta-fuse FUSE-mount (3 tests) | Tests use `docker exec meta-fuse` but the container is named `metafuse-app` (Yundera prod-parity rename) | Unrelated — predates this work |
| meta-fuse WebDAV PROPFIND (4 tests) | Tests use Caddy URL with WebDAV basic-auth credentials that no longer match the hash-lock perimeter | Unrelated — predates this work |
| meta-sort "API integration tests" (1 test) | Test runs `docker exec meta-sort pnpm test`; container is `metasort-app` | Unrelated — predates this work |
| meta-core "create and delete mount config" (1 test) | Test creates an NFS mount; production code dropped NFS support ("NFS mounts are no longer supported") | Unrelated — predates this work |
| service-discovery (9 tests) | Cascading from meta-stremio breakage (test expects unauth `/api/services` on stremio) | Unrelated to lockdown directly |
| meta-share / share-1c (multiple) | Tests assert `"phase":"1f"` in `/health`; meta-share has moved past that phase | Unrelated — meta-share evolution |
| flat-keys: "filePath field exists" (1 test) | Test SRANDMEMBERs file:__index__; if it lands on a UUID with no filePath (e.g. a manual test write) it fails | Was hit transiently after my interactive testing left stub entries; resolved by cleanup |

No failure traceable to a real regression from this work was found.
The dev-environment test harness needs an unrelated refresh to catch
up with the prod-parity container renames, the NFS removal, the
STREMIO_HASH_API_SEED default, and meta-share's current phase.
