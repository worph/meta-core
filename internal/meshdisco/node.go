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
	"golang.org/x/sys/unix"
)

// Config configures a Node. Payload is a function rather than a value so the
// advertise is rebuilt from live state on every tick — that is what keeps the
// advertised core URLs and GET /urls from drifting apart.
type Config struct {
	Name     string
	Instance string
	Version  string

	Group    string
	Port     int
	Interval time.Duration

	// Payload returns this node's status ("starting" / "running") and its
	// resources. The envelope and NodeInfo are filled in by the Node.
	Payload func() (status string, resources []Resource)
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
}

type seen struct {
	node      NodeInfo
	resources []Resource
	addr      string
	lastSeen  time.Time
}

// Node is one beacon v2 participant: it advertises its resources, answers
// probes, and keeps a TTL map of every other node it has heard from.
type Node struct {
	cfg   Config
	group net.IP

	pc     *ipv4.PacketConn
	closer interface{ Close() error }

	// ifaces are the multicast-capable interfaces we joined and send on.
	// A container attached to two docker networks (metashare-app on `pcs`
	// and `metamesh-mesh`, metawatch-* on `metawatch` and `metamesh-mesh`)
	// has two entries here, and MUST be advertised on both — sending via the
	// default route alone silently covers only one network.
	ifaces []net.Interface

	// sendMu serialises SetMulticastInterface + WriteTo, which are a
	// two-step operation on one shared socket.
	sendMu sync.Mutex

	mu    sync.RWMutex
	nodes map[string]*seen

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New creates a Node. It does not touch the network until Start.
func New(cfg Config) *Node {
	cfg.applyDefaults()
	return &Node{
		cfg:      cfg,
		nodes:    make(map[string]*seen),
		stopChan: make(chan struct{}),
	}
}

// Start opens the multicast socket, joins the group on every eligible
// interface, and begins advertising. A join failure on one interface is logged
// and skipped rather than fatal — a host that blocks multicast degrades
// instead of failing to boot.
func (n *Node) Start() error {
	n.group = net.ParseIP(n.cfg.Group)
	if n.group == nil || n.group.To4() == nil {
		return fmt.Errorf("beacon: invalid multicast group %q", n.cfg.Group)
	}

	lc := net.ListenConfig{Control: reusePort}
	conn, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", n.cfg.Port))
	if err != nil {
		return fmt.Errorf("beacon: listen on :%d: %w", n.cfg.Port, err)
	}
	n.closer = conn

	pc := ipv4.NewPacketConn(conn)
	// TTL 1: link-local only. Discovery never routes off the local segment;
	// the two-host topology keeps two separate meshes by design.
	if err := pc.SetMulticastTTL(1); err != nil {
		log.Printf("[beacon] Warning: could not set multicast TTL: %v", err)
	}
	n.pc = pc

	n.ifaces = eligibleInterfaces()
	if len(n.ifaces) == 0 {
		log.Println("[beacon] Warning: no multicast-capable interfaces found; discovery will be inert")
	}

	group := &net.UDPAddr{IP: n.group}
	joined := 0
	for i := range n.ifaces {
		if err := pc.JoinGroup(&n.ifaces[i], group); err != nil {
			log.Printf("[beacon] Warning: join %s on %s failed: %v", n.cfg.Group, n.ifaces[i].Name, err)
			continue
		}
		joined++
	}
	log.Printf("[beacon] Listening on %s:%d as %s/%s (joined %d of %d interfaces)",
		n.cfg.Group, n.cfg.Port, n.cfg.Name, n.cfg.Instance, joined, len(n.ifaces))

	n.wg.Add(2)
	go n.readLoop()
	go n.advertiseLoop()

	return nil
}

// Stop multicasts a bye — so neighbours drop us immediately instead of
// waiting out the staleness window — then closes the socket and waits for the
// loops to exit.
func (n *Node) Stop() error {
	var err error
	n.stopOnce.Do(func() {
		n.broadcast(Message{Proto: Proto, V: Version, Type: TypeBye, Node: n.nodeInfo("")})
		close(n.stopChan)
		if n.closer != nil {
			err = n.closer.Close()
		}
		n.wg.Wait()
	})
	return err
}

// Probe multicasts a probe on every interface. Every node owning a resource
// matching one of want (every node, when want is empty) replies immediately,
// so a caller does not have to wait out an interval.
func (n *Node) Probe(want ...string) {
	n.broadcast(Message{Proto: Proto, V: Version, Type: TypeProbe, From: n.cfg.Instance, Want: want})
}

// Neighbors returns every node heard from within the staleness window,
// sorted by (name, instance). Expired entries are dropped as a side effect —
// expiry is a read-time concern, so there is no reaper goroutine.
func (n *Node) Neighbors() []Neighbor {
	cutoff := time.Now().Add(-n.cfg.Interval * LivenessFactor)

	n.mu.Lock()
	out := make([]Neighbor, 0, len(n.nodes))
	for k, s := range n.nodes {
		if s.lastSeen.Before(cutoff) {
			delete(n.nodes, k)
			continue
		}
		out = append(out, newNeighbor(s.node, s.resources, s.addr, s.lastSeen))
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

// NeighborsByName collapses multiple instances of one name down to the most
// recently seen — the shape the nav menu wants.
func (n *Node) NeighborsByName() []Neighbor {
	best := make(map[string]Neighbor)
	for _, nb := range n.Neighbors() {
		if cur, ok := best[nb.Name]; !ok || nb.LastSeen > cur.LastSeen {
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

// Self returns this node as a row, so callers can render themselves without
// waiting to hear their own multicast echo back.
func (n *Node) Self() Neighbor {
	status, resources := n.payload()
	return newNeighbor(*n.nodeInfo(status), resources, "", time.Now())
}

func (n *Node) payload() (string, []Resource) {
	if n.cfg.Payload == nil {
		return "running", nil
	}
	return n.cfg.Payload()
}

func (n *Node) nodeInfo(status string) *NodeInfo {
	return &NodeInfo{Name: n.cfg.Name, Instance: n.cfg.Instance, Version: n.cfg.Version, Status: status}
}

func (n *Node) advertiseMsg() Message {
	status, resources := n.payload()
	return Message{Proto: Proto, V: Version, Type: TypeAdvertise, Node: n.nodeInfo(status), Resources: resources}
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
			log.Printf("[beacon] Read error: %v", err)
			continue
		}

		msg, ok := Parse(buf[:nRead])
		if !ok {
			continue // v1 or foreign traffic on the shared group
		}

		switch msg.Type {
		case TypeProbe:
			if msg.From == n.cfg.Instance {
				continue
			}
			ad := n.advertiseMsg()
			wanted := len(msg.Want) == 0
			for _, r := range ad.Resources {
				if r.MatchesAny(msg.Want) {
					wanted = true
					break
				}
			}
			if wanted {
				// Unicast straight back to the prober's source address; its
				// ephemeral source port is where it is listening.
				n.send(ad, src)
			}
		case TypeAdvertise, TypeBye:
			// Drop our own multicast echo. Loopback stays enabled so a
			// single-container test still sees traffic; we filter by identity.
			if msg.Node.Instance == n.cfg.Instance {
				continue
			}
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

	n.mu.Lock()
	defer n.mu.Unlock()
	if msg.Type == TypeBye {
		delete(n.nodes, msg.Node.Instance)
		return
	}
	n.nodes[msg.Node.Instance] = &seen{node: *msg.Node, resources: msg.Resources, addr: addr, lastSeen: time.Now()}
}

func (n *Node) advertiseLoop() {
	defer n.wg.Done()

	// Advertise immediately, then probe once so the map fills without
	// waiting a full interval for everyone else's ticker.
	n.broadcast(n.advertiseMsg())
	n.Probe()

	ticker := time.NewTicker(n.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-n.stopChan:
			return
		case <-ticker.C:
			n.broadcast(n.advertiseMsg())
		}
	}
}

func encode(msg Message) ([]byte, bool) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, false
	}
	if len(body) > MaxDatagram {
		log.Printf("[beacon] Warning: %d-byte datagram exceeds the fragmentation-safe %d", len(body), MaxDatagram)
	}
	return body, true
}

// send unicasts msg to dst (a probe reply).
func (n *Node) send(msg Message, dst net.Addr) {
	body, ok := encode(msg)
	if !ok {
		return
	}
	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	if _, err := n.pc.WriteTo(body, nil, dst); err != nil {
		log.Printf("[beacon] Reply to %s failed: %v", dst, err)
	}
}

// broadcast writes one message to the group once per eligible interface.
// Writing once and letting the routing table choose would reach exactly one
// network, which is the failure mode this protocol exists to avoid.
func (n *Node) broadcast(msg Message) {
	if n.pc == nil {
		return
	}
	body, ok := encode(msg)
	if !ok {
		return
	}
	dst := &net.UDPAddr{IP: n.group, Port: n.cfg.Port}

	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	for i := range n.ifaces {
		if err := n.pc.SetMulticastInterface(&n.ifaces[i]); err != nil {
			log.Printf("[beacon] Warning: select interface %s failed: %v", n.ifaces[i].Name, err)
			continue
		}
		if _, err := n.pc.WriteTo(body, nil, dst); err != nil {
			log.Printf("[beacon] Warning: send on %s failed: %v", n.ifaces[i].Name, err)
		}
	}
}

// eligibleInterfaces returns every up, non-loopback, multicast-capable
// interface that has an IPv4 address.
func eligibleInterfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		log.Printf("[beacon] Warning: could not enumerate interfaces: %v", err)
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

// reusePort sets SO_REUSEADDR + SO_REUSEPORT so several listeners (a test, a
// sidecar, a beacon v1 responder in the same netns) can bind 9099 on one host.
func reusePort(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
		if serr == nil {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
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
