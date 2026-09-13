package meshdisco

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

// Config configures a Node. Payload is a function rather than a value so the
// announce is rebuilt from live state on every tick — that is what keeps the
// announced URLs and GET /urls from drifting apart.
type Config struct {
	Name     string
	Instance string
	Role     string
	Version  string

	Group    string
	Port     int
	Interval time.Duration

	// Payload returns the identity/URL fields for this announce. Name,
	// Instance, Role, Version and the envelope are filled in by the Node.
	Payload func() (baseURL string, status string, urls *URLs)
}

func (c *Config) applyDefaults() {
	if c.Group == "" {
		c.Group = DefaultGroup
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.Role == "" {
		c.Role = RoleService
	}
}

// Node is one participant in the mesh: it announces itself, answers probes,
// and keeps a TTL map of everyone else it has heard from.
type Node struct {
	cfg   Config
	group net.IP

	pc     *ipv4.PacketConn
	closer interface{ Close() error }

	// ifaces are the multicast-capable interfaces we joined and send on.
	// A container attached to two docker networks (metashare-app on `pcs`
	// and `metamesh-mesh`, metawatch-* on `metawatch` and `metamesh-mesh`)
	// has two entries here, and MUST be announced on both — sending via the
	// default route alone silently covers only one network.
	ifaces []net.Interface

	// sendMu serialises SetMulticastInterface + WriteTo, which are a
	// two-step operation on one shared socket.
	sendMu sync.Mutex

	mu        sync.RWMutex
	neighbors map[string]*Neighbor

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New creates a Node. It does not touch the network until Start.
func New(cfg Config) *Node {
	cfg.applyDefaults()
	return &Node{
		cfg:       cfg,
		neighbors: make(map[string]*Neighbor),
		stopChan:  make(chan struct{}),
	}
}

// Start opens the multicast socket, joins the group on every eligible
// interface, and begins announcing. A join failure on one interface is logged
// and skipped rather than fatal — matching the ENABLE_MDNS precedent in the
// Rust services, where a host that blocks multicast degrades instead of
// failing to boot.
func (n *Node) Start() error {
	n.group = net.ParseIP(n.cfg.Group)
	if n.group == nil || n.group.To4() == nil {
		return fmt.Errorf("meshdisco: invalid multicast group %q", n.cfg.Group)
	}

	lc := net.ListenConfig{Control: reusePort}
	conn, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", n.cfg.Port))
	if err != nil {
		return fmt.Errorf("meshdisco: listen on :%d: %w", n.cfg.Port, err)
	}
	n.closer = conn

	pc := ipv4.NewPacketConn(conn)
	// TTL 1: link-local only. Discovery never routes off the local segment;
	// the two-host topology keeps two separate meshes by design.
	if err := pc.SetMulticastTTL(1); err != nil {
		log.Printf("[meshdisco] Warning: could not set multicast TTL: %v", err)
	}
	n.pc = pc

	n.ifaces = eligibleInterfaces()
	if len(n.ifaces) == 0 {
		log.Println("[meshdisco] Warning: no multicast-capable interfaces found; discovery will be inert")
	}

	group := &net.UDPAddr{IP: n.group}
	joined := 0
	for i := range n.ifaces {
		if err := pc.JoinGroup(&n.ifaces[i], group); err != nil {
			log.Printf("[meshdisco] Warning: join %s on %s failed: %v", n.cfg.Group, n.ifaces[i].Name, err)
			continue
		}
		joined++
	}
	log.Printf("[meshdisco] Listening on %s:%d as %s/%s (joined %d of %d interfaces)",
		n.cfg.Group, n.cfg.Port, n.cfg.Name, n.cfg.Instance, joined, len(n.ifaces))

	n.wg.Add(2)
	go n.readLoop()
	go n.announceLoop()

	return nil
}

// Stop closes the socket and waits for the loops to exit. It sends a final
// announce with status "stopping" so neighbours drop us immediately instead of
// waiting out the staleness window.
func (n *Node) Stop() error {
	var err error
	n.stopOnce.Do(func() {
		n.sendAnnounce(nil, "stopping")
		close(n.stopChan)
		if n.closer != nil {
			err = n.closer.Close()
		}
		n.wg.Wait()
	})
	return err
}

// Probe multicasts a discovery message on every interface. Every listener
// replies immediately, so a caller does not have to wait out an announce
// interval. This is what makes the protocol usable on the boot path.
func (n *Node) Probe() {
	n.broadcast(Message{V: ProtocolVersion, Type: TypeDiscovery})
}

// Neighbors returns everyone heard from within the staleness window, sorted by
// name. Entries older than that are dropped as a side effect — expiry is a
// read-time concern, so there is no reaper goroutine and no equivalent of the
// old cleaner.go.
func (n *Node) Neighbors() []Neighbor {
	cutoff := time.Now().Add(-n.cfg.Interval * LivenessFactor)

	n.mu.Lock()
	out := make([]Neighbor, 0, len(n.neighbors))
	for k, nb := range n.neighbors {
		if nb.LastSeen.Before(cutoff) {
			delete(n.neighbors, k)
			continue
		}
		out = append(out, *nb)
	}
	n.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Instance < out[j].Instance
	})
	return out
}

// NeighborsByName collapses multiple instances of one service down to the most
// recently seen, which is the shape the nav menu wants (and what the existing
// /api/services dedup contract asserts).
func (n *Node) NeighborsByName() []Neighbor {
	best := make(map[string]Neighbor)
	for _, nb := range n.Neighbors() {
		if cur, ok := best[nb.Name]; !ok || nb.LastSeen.After(cur.LastSeen) {
			best[nb.Name] = nb
		}
	}
	out := make([]Neighbor, 0, len(best))
	for _, nb := range best {
		out = append(out, nb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Self returns this node's own announce, so callers can render themselves in
// the menu without waiting to hear their own multicast echo back.
func (n *Node) Self() Neighbor {
	return Neighbor{Message: n.buildAnnounce("running"), LastSeen: time.Now()}
}

func (n *Node) readLoop() {
	defer n.wg.Done()
	buf := make([]byte, ReadBufferSize)

	for {
		select {
		case <-n.stopChan:
			return
		default:
		}

		nRead, _, src, err := n.pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-n.stopChan:
				return
			default:
			}
			log.Printf("[meshdisco] Read error: %v", err)
			continue
		}

		var msg Message
		if err := json.Unmarshal(buf[:nRead], &msg); err != nil {
			// Malformed or foreign traffic on the group — ignore quietly.
			continue
		}
		if msg.V != ProtocolVersion {
			continue
		}
		// Drop our own multicast echo. Loopback is left enabled so that a
		// single-container test still sees traffic; we filter by identity
		// instead of relying on the socket option.
		if msg.Instance != "" && msg.Instance == n.cfg.Instance && msg.Name == n.cfg.Name {
			continue
		}

		switch msg.Type {
		case TypeDiscovery:
			// Unicast the reply straight back to the prober's source
			// address. The kernel picks the right interface by route, and
			// the prober's ephemeral source port is where it is listening.
			n.sendAnnounce(src, "running")
		case TypeAnnounce:
			n.record(msg, src)
		}
	}
}

func (n *Node) record(msg Message, src net.Addr) {
	addr := ""
	if src != nil {
		if udp, ok := src.(*net.UDPAddr); ok {
			addr = udp.IP.String()
		} else {
			addr = src.String()
		}
	}

	if msg.Status == "stopping" {
		n.mu.Lock()
		delete(n.neighbors, msg.key())
		n.mu.Unlock()
		return
	}

	nb := &Neighbor{Message: msg, Addr: addr, LastSeen: time.Now()}
	n.mu.Lock()
	n.neighbors[msg.key()] = nb
	n.mu.Unlock()
}

func (n *Node) announceLoop() {
	defer n.wg.Done()

	// Announce immediately, then probe once so the map fills without waiting
	// a full interval for everyone else's ticker.
	n.sendAnnounce(nil, "running")
	n.Probe()

	ticker := time.NewTicker(n.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-n.stopChan:
			return
		case <-ticker.C:
			n.sendAnnounce(nil, "running")
		}
	}
}

func (n *Node) buildAnnounce(status string) Message {
	msg := Message{
		V:        ProtocolVersion,
		Type:     TypeAnnounce,
		Name:     n.cfg.Name,
		Instance: n.cfg.Instance,
		Role:     n.cfg.Role,
		Version:  n.cfg.Version,
		Status:   status,
	}
	if n.cfg.Payload != nil {
		baseURL, st, urls := n.cfg.Payload()
		msg.BaseUrl = baseURL
		if st != "" && status == "running" {
			msg.Status = st
		}
		// Only meta-core carries a URLs block; a service announcing one
		// would let any container on the network impersonate the core.
		if n.cfg.Role == RoleCore {
			msg.URLs = urls
		}
	}
	return msg
}

// sendAnnounce multicasts when dst is nil, or unicasts to dst (a probe reply).
func (n *Node) sendAnnounce(dst net.Addr, status string) {
	msg := n.buildAnnounce(status)
	if dst == nil {
		n.broadcast(msg)
		return
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	if _, err := n.pc.WriteTo(body, nil, dst); err != nil {
		log.Printf("[meshdisco] Reply to %s failed: %v", dst, err)
	}
}

// broadcast writes one message to the group once per eligible interface.
// Writing once and letting the routing table choose would reach exactly one
// network, which is the failure mode this protocol exists to avoid.
func (n *Node) broadcast(msg Message) {
	if n.pc == nil {
		return
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return
	}
	dst := &net.UDPAddr{IP: n.group, Port: n.cfg.Port}

	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	for i := range n.ifaces {
		if err := n.pc.SetMulticastInterface(&n.ifaces[i]); err != nil {
			log.Printf("[meshdisco] Warning: select interface %s failed: %v", n.ifaces[i].Name, err)
			continue
		}
		if _, err := n.pc.WriteTo(body, nil, dst); err != nil {
			log.Printf("[meshdisco] Warning: send on %s failed: %v", n.ifaces[i].Name, err)
		}
	}
}

// eligibleInterfaces returns every up, non-loopback, multicast-capable
// interface that has an IPv4 address.
func eligibleInterfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		log.Printf("[meshdisco] Warning: could not enumerate interfaces: %v", err)
		return nil
	}

	var out []net.Interface
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 ||
			ifi.Flags&net.FlagLoopback != 0 ||
			ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				out = append(out, ifi)
				break
			}
		}
	}
	return out
}

// reusePort sets SO_REUSEADDR so a second listener (a test, or a sidecar) can
// bind the same multicast port on the same host.
func reusePort(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err
	}
	return serr
}

// LocalIPv4 returns this host's first non-loopback IPv4 address, or the
// hostname when none can be determined. Same fallback the file-based registry
// used (discovery/service.go), kept identical so announced URLs don't shift.
func LocalIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String()
				}
			}
		}
	}
	host, _ := os.Hostname()
	return host
}
