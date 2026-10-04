package meshdisco

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestCapMatches(t *testing.T) {
	cases := []struct {
		pattern, cap string
		want         bool
	}{
		{"metamesh.transport/*", "metamesh.transport/nzb@1", true},
		{"metamesh.transport/*", "metamesh.transport/ipfs", true},
		{"metamesh.transport/*", "metamesh.transport", false},
		{"metamesh.transport/*", "metamesh.transportx/a", false},
		{"metamesh.transport/*", "metamesh.transport/", false},
		{"metamesh.transport/nzb", "metamesh.transport/nzb@2", true},
		{"metamesh.transport/nzb@2", "metamesh.transport/nzb@2", true},
		{"metamesh.transport/nzb@2", "metamesh.transport/nzb@1", false},
		{"metamesh.transport/nzb@2", "metamesh.transport/nzb", false},
		{"metamesh.transport/*@1", "metamesh.transport/nzb@1", true},
		{"metamesh.core", "metamesh.core", true},
		{"metamesh.core", "metamesh.core2", false},
		{"mcp", "mcp/x", false},
		{"*", "anything@3", true},
	}
	for _, c := range cases {
		if got := CapMatches(c.pattern, c.cap); got != c.want {
			t.Errorf("CapMatches(%q, %q) = %v, want %v", c.pattern, c.cap, got, c.want)
		}
	}
}

func TestParseRejectsV1AndForeign(t *testing.T) {
	for _, raw := range []string{
		`{"type":"discovery"}`,
		`{"type":"announce","name":"x","tools":[]}`,
		`{"v":1,"type":"announce","name":"meta-sort"}`,
		`{"proto":"beacon","v":3,"type":"probe"}`,
		`{"proto":"beacon","v":2,"type":"announce"}`,
		`{"proto":"beacon","v":2,"type":"advertise"}`,
		"\x00garbage",
	} {
		if _, ok := Parse([]byte(raw)); ok {
			t.Errorf("Parse(%q) accepted", raw)
		}
	}
	if _, ok := Parse([]byte(`{"proto":"beacon","v":2,"type":"probe","want":["metamesh.core"]}`)); !ok {
		t.Error("valid probe rejected")
	}
}

func TestNeighborRowCarriesCapsAndBaseUrl(t *testing.T) {
	nb := newNeighbor(
		NodeInfo{Name: "meta-sort", Instance: "metasort-app"},
		[]Resource{{ID: "service", Caps: []string{"metamesh.service/meta-sort"}, Endpoints: map[string]string{"ui": "https://x"}}},
		"10.0.0.1", time.Now(),
	)
	if nb.BaseUrl != "https://x" || len(nb.Caps) != 1 || !nb.Matches(CapAnyService) {
		t.Fatalf("unexpected row: %+v", nb)
	}
}

// End to end over loopback: a probe with a non-matching want gets no reply;
// a matching one gets the advertise; advertise then bye updates the view.
func TestProbeAndRecord(t *testing.T) {
	port := freePort(t)
	n := New(Config{
		Name: "meta-core", Instance: "core-1", Port: port, Interval: time.Hour,
		Payload: func() (string, []Resource) {
			return "running", []Resource{{ID: "core", Caps: []string{CapCore}}}
		},
	})
	if err := n.Start(); err != nil {
		t.Fatal(err)
	}
	defer n.Stop()

	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	dst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	buf := make([]byte, ReadBufferSize)

	send := func(m Message) {
		b, _ := json.Marshal(m)
		if _, err := c.WriteToUDP(b, dst); err != nil {
			t.Fatal(err)
		}
	}

	send(Message{Proto: Proto, V: Version, Type: TypeProbe, From: "cli", Want: []string{"metamesh.service/*"}})
	c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := c.ReadFromUDP(buf); err == nil {
		t.Fatal("non-matching probe got a reply")
	}

	send(Message{Proto: Proto, V: Version, Type: TypeProbe, From: "cli", Want: []string{CapCore}})
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	nr, _, err := c.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("matching probe got no reply: %v", err)
	}
	if m, ok := Parse(buf[:nr]); !ok || m.Type != TypeAdvertise || m.Node.Instance != "core-1" {
		t.Fatalf("bad reply: %s", buf[:nr])
	}

	peer := &NodeInfo{Name: "meta-sort", Instance: "sort-1"}
	send(Message{Proto: Proto, V: Version, Type: TypeAdvertise, Node: peer,
		Resources: []Resource{{ID: "service", Caps: []string{"metamesh.service/meta-sort"}}}})
	waitFor(t, func() bool { return len(n.Neighbors()) == 1 })
	send(Message{Proto: Proto, V: Version, Type: TypeBye, Node: peer})
	waitFor(t, func() bool { return len(n.Neighbors()) == 0 })
}

func freePort(t *testing.T) int {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
