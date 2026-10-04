// Package meshdisco implements beacon v2: local resource advertise / discover
// over UDP multicast (239.255.99.1:9099), the protocol every meta-* service
// and SDK-hosted plugin uses to find its neighbours.
//
// A node advertises Resources, each tagged with capability strings
// ("metamesh.core", "metamesh.service/meta-sort", "metamesh.transport/nzb@1");
// consumers keep a live view of everyone else's and filter by capability.
// meta-core advertises "metamesh.core" carrying its /urls block, which is how
// services locate it without a shared volume.
//
// The wire protocol is documented in docs/project-architecture/beacon-v2.md.
// Keep this package and that doc in step — the Rust (meta-feeder-sdk),
// TypeScript and Python implementations are written against the doc.
package meshdisco

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	// Proto and Version form the envelope every v2 message carries. Anything
	// else on the group (beacon v1, junk) is dropped silently.
	Proto   = "beacon"
	Version = 2

	// DefaultGroup / DefaultPort are beacon's endpoint, on purpose: one
	// well-known place for every kind of local resource. v1 and v2 coexist
	// because v2 never sends v1's "discovery" / "announce" types.
	DefaultGroup = "239.255.99.1"
	DefaultPort  = 9099

	// DefaultInterval is how often an unsolicited advertise is emitted.
	DefaultInterval = 10 * time.Second

	// LivenessFactor multiplies the interval to get the staleness window.
	// Expiry is evaluated at read time; there is no reaper.
	LivenessFactor = 3

	// ReadBufferSize is the receive buffer; MaxDatagram is what a sender
	// stays under so nothing fragments.
	ReadBufferSize = 8192
	MaxDatagram    = 1400

	TypeProbe     = "probe"
	TypeAdvertise = "advertise"
	TypeBye       = "bye"

	// CapCore is meta-core's capability; its resource carries Data.urls.
	CapCore = "metamesh.core"
	// CapServicePrefix + name is every service's capability.
	CapServicePrefix = "metamesh.service/"
	CapAnyService    = "metamesh.service/*"
)

// URLs mirrors meta-core's GET /urls response (api.URLsResponse) minus
// redisUrl. It travels as the metamesh.core resource's Data.urls, so a service
// locates meta-core from the advertise alone.
type URLs struct {
	Hostname          string `json:"hostname"`
	BaseUrl           string `json:"baseUrl"`
	ApiUrl            string `json:"apiUrl"`
	WebdavUrl         string `json:"webdavUrl"`
	WebdavUrlInternal string `json:"webdavUrlInternal"`
}

// NodeInfo says who is advertising.
type NodeInfo struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	Version  string `json:"version,omitempty"`
	// Status is "starting" | "running"; absent reads as running.
	Status string `json:"status,omitempty"`
}

// Resource is one advertised thing.
type Resource struct {
	ID   string   `json:"id"`
	Caps []string `json:"caps"`
	// Endpoints: well-known names http, ui, manifest, mcp. A value starting
	// with "/" is relative to endpoints.http.
	Endpoints map[string]string `json:"endpoints,omitempty"`
	Rev       string            `json:"rev,omitempty"`
	// Binds names the one consumer instance this resource belongs to.
	Binds string          `json:"binds,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Matches reports whether any cap matches pattern.
func (r Resource) Matches(pattern string) bool {
	for _, c := range r.Caps {
		if CapMatches(pattern, c) {
			return true
		}
	}
	return false
}

// MatchesAny reports whether any cap matches any pattern; an empty list matches.
func (r Resource) MatchesAny(patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if r.Matches(p) {
			return true
		}
	}
	return false
}

// Endpoint returns the named endpoint as an absolute URL.
func (r Resource) Endpoint(name string) string {
	v := r.Endpoints[name]
	if strings.HasPrefix(v, "/") {
		base := r.Endpoints["http"]
		if base == "" {
			return ""
		}
		return strings.TrimRight(base, "/") + v
	}
	return v
}

// Message is every v2 datagram.
type Message struct {
	Proto     string     `json:"proto"`
	V         int        `json:"v"`
	Type      string     `json:"type"`
	Node      *NodeInfo  `json:"node,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
	// From and Want are probe-only.
	From string   `json:"from,omitempty"`
	Want []string `json:"want,omitempty"`
}

// Parse decodes a datagram, returning ok=false for anything that is not a
// well-formed beacon v2 message (v1 traffic, other versions, unknown types,
// an advertise/bye without a node).
func Parse(b []byte) (Message, bool) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return m, false
	}
	if m.Proto != Proto || m.V != Version {
		return m, false
	}
	switch m.Type {
	case TypeProbe:
		return m, true
	case TypeAdvertise, TypeBye:
		if m.Node == nil || m.Node.Name == "" || m.Node.Instance == "" {
			return m, false
		}
		return m, true
	}
	return m, false
}

func splitContract(s string) (string, string, bool) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// CapMatches reports whether capability cap satisfies pattern:
// "*" matches everything; an "@N" on the pattern must equal the cap's
// contract (none on the pattern ignores it); "x/*" matches any variant of x;
// otherwise the bases must be equal.
func CapMatches(pattern, cap string) bool {
	if pattern == "*" {
		return true
	}
	pbase, pc, phas := splitContract(pattern)
	cbase, cc, chas := splitContract(cap)
	if phas && (!chas || pc != cc) {
		return false
	}
	if prefix, ok := strings.CutSuffix(pbase, "/*"); ok {
		rest, ok := strings.CutPrefix(cbase, prefix)
		return ok && strings.HasPrefix(rest, "/") && len(rest) > 1
	}
	return pbase == cbase
}

// Neighbor is a node heard on the wire, as served by /api/neighbors.
type Neighbor struct {
	NodeInfo
	Resources []Resource `json:"resources"`
	// Caps flattens every resource's caps, for consumers that just filter.
	Caps []string `json:"caps"`
	// BaseUrl is the metamesh.service/* resource's endpoints.ui — kept so
	// v1-era readers (the nav menu) keep working.
	BaseUrl string `json:"baseUrl,omitempty"`
	// Addr is the packet's source address, never a payload claim.
	Addr string `json:"addr"`
	// LastSeen is unix seconds.
	LastSeen float64 `json:"lastSeen"`
}

// Matches reports whether any resource matches pattern.
func (n Neighbor) Matches(pattern string) bool {
	for _, r := range n.Resources {
		if r.Matches(pattern) {
			return true
		}
	}
	return false
}

func newNeighbor(node NodeInfo, resources []Resource, addr string, seen time.Time) Neighbor {
	nb := Neighbor{
		NodeInfo:  node,
		Resources: resources,
		Caps:      []string{},
		Addr:      addr,
		LastSeen:  float64(seen.UnixNano()) / 1e9,
	}
	if nb.Resources == nil {
		nb.Resources = []Resource{}
	}
	seenCap := map[string]bool{}
	for _, r := range resources {
		for _, c := range r.Caps {
			if !seenCap[c] {
				seenCap[c] = true
				nb.Caps = append(nb.Caps, c)
			}
		}
		if nb.BaseUrl == "" && r.Matches(CapAnyService) {
			nb.BaseUrl = r.Endpoint("ui")
		}
	}
	return nb
}
