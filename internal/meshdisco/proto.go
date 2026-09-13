// Package meshdisco implements meta-discovery v1: UDP multicast service
// discovery for the meta-* services.
//
// It replaces two file-based mechanisms that both required a shared
// /meta-core volume:
//
//   - /meta-core/locks/kv-leader.info — a one-line file holding meta-core's
//     API URL, read at boot to locate meta-core.
//   - /meta-core/services/<name>-<host>.json — per-service registration files
//     aggregated into the dashboard's service nav.
//
// Both answer the same question — where is meta-core, and who else is on this
// network — so one announce packet answers both. Discovery scope becomes the
// docker network the container is attached to rather than the volume it
// mounts, which is what lets per-app CasaOS stacks and the meta-watch island
// see their neighbours without sharing storage.
//
// The wire protocol is documented in
// docs/project-architecture/service-discovery.md. Keep this file and that doc
// in step — every other language implementation is written against the doc.
package meshdisco

import "time"

const (
	// ProtocolVersion is the `v` field on every message. Receivers ignore
	// messages carrying a version they don't know.
	ProtocolVersion = 1

	// DefaultGroup is the IPv4 multicast group. Deliberately NOT beacon's
	// 239.255.99.1 (sandbox/beacon), so the two protocols cannot cross-talk
	// if they ever share a network.
	DefaultGroup = "239.255.77.1"

	// DefaultPort is the UDP port. Deliberately NOT beacon's 9099, for the
	// same reason.
	DefaultPort = 9399

	// DefaultInterval is how often an unsolicited announce is emitted.
	DefaultInterval = 10 * time.Second

	// LivenessFactor multiplies the announce interval to get the staleness
	// window: a neighbour last seen longer ago than interval*factor is
	// dropped. This replaces the old heartbeat file + cleaner.go reaper.
	LivenessFactor = 3

	// ReadBufferSize is the datagram receive buffer. Beacon's reference
	// implementation uses 1 KiB, which truncates a realistic payload; an
	// announce carrying the full URLs block is comfortably under 8 KiB.
	ReadBufferSize = 8192

	// TypeDiscovery is a probe: "who is out there?". Sent at startup and on
	// a manual refresh, answered immediately by every listener.
	TypeDiscovery = "discovery"

	// TypeAnnounce is a service describing itself, sent unsolicited on a
	// ticker and unicast in reply to a probe.
	TypeAnnounce = "announce"

	// RoleCore marks meta-core itself — the only role that carries a URLs
	// block. Consumers locating meta-core filter on this.
	RoleCore = "core"

	// RoleService marks every other meta-* service.
	RoleService = "service"
)

// URLs mirrors meta-core's existing GET /urls response (api.URLsResponse)
// minus redisUrl, which the api-mediated-access lockdown retired. Embedding it
// in the announce makes the announce a drop-in for the /urls hop that
// LeaderClient performs today; /urls itself stays, since meta-search and
// meta-share still call it.
type URLs struct {
	Hostname          string `json:"hostname"`
	BaseUrl           string `json:"baseUrl"`
	ApiUrl            string `json:"apiUrl"`
	WebdavUrl         string `json:"webdavUrl"`
	WebdavUrlInternal string `json:"webdavUrlInternal"`
}

// Message is the single wire type: both probe and announce are one JSON
// datagram. Everything but `v` and `type` is omitted on a probe.
type Message struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	// Name is the service name, e.g. "meta-core", "meta-sort".
	Name string `json:"name,omitempty"`
	// Instance distinguishes two instances of the same Name — the container
	// hostname. Also used to recognise and drop our own multicast echo.
	Instance string `json:"instance,omitempty"`
	// Role is RoleCore or RoleService.
	Role string `json:"role,omitempty"`
	// Version is the service's build version, for display only.
	Version string `json:"version,omitempty"`
	// Status is "running" | "starting" | "stopping".
	Status string `json:"status,omitempty"`

	// BaseUrl is the browser-facing URL the nav menu links to. Built with
	// the same three-way fallback the file registry used:
	// META_CORE_PUBLIC_URL -> BASE_URL -> http://<local-ip>:<port>.
	BaseUrl string `json:"baseUrl,omitempty"`

	// URLs is populated only when Role == RoleCore.
	URLs *URLs `json:"urls,omitempty"`

	// Token is reserved for a future HMAC over the payload. v1 senders omit
	// it and v1 receivers ignore it; see the doc's Security section.
	Token string `json:"token,omitempty"`
}

// Neighbor is a Message plus the receive-side facts the sender cannot state
// about itself.
type Neighbor struct {
	Message
	// Addr is the source address the datagram arrived from. Taken from the
	// packet, never from the payload — a sender does not get to claim its
	// own IP.
	Addr string `json:"addr"`
	// LastSeen is when we last received an announce from this instance.
	LastSeen time.Time `json:"lastSeen"`
}

// key identifies a neighbour entry. Two instances of the same service are
// distinct neighbours; callers that want one row per service (the nav menu)
// collapse by Name themselves.
func (m Message) key() string {
	if m.Instance == "" {
		return m.Name
	}
	return m.Name + "|" + m.Instance
}
