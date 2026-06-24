package swim_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/swim"
)

// cluster manages a group of swim.Members for testing.
// Every node is instrumented with a [recordingNotifier].
type cluster struct {
	t              *testing.T
	members        []*swim.Member
	network        *network
	notifiers      []*recordingNotifier
	protocolPeriod time.Duration
	ackTimeout     time.Duration
}

func newCluster(t *testing.T, nodes int) *cluster {
	t.Helper()

	const (
		protocolPeriod = 1 * time.Second
		ackTimeout     = 500 * time.Millisecond
	)

	notifiers := make([]*recordingNotifier, nodes)
	for i := range nodes {
		notifiers[i] = &recordingNotifier{}
	}
	machineID := func(i int) string { return fmt.Sprintf("machine-%d", i) }
	hosts := make([]string, nodes)
	for i := range nodes {
		hosts[i] = machineID(i)
	}
	network := newNetwork(t, hosts)
	rt := newJoinRoundTripper(network)
	members := make([]*swim.Member, nodes)
	for i := range nodes {
		seeds := make([]string, 0, nodes-1)
		for j := range nodes {
			if i != j {
				seeds = append(seeds, fmt.Sprintf("node-%d:7947", j))
			}
		}
		cfg := swim.Config{
			NodeID:         fmt.Sprintf("node-%d", i),
			AdvertiseHost:  machineID(i),
			Conn:           network.conn(i),
			Listener:       newFakeListener(),
			Seeds:          strings.Join(seeds, ","),
			Resolve:        network.resolve,
			ProtocolPeriod: protocolPeriod,
			AckTimeout:     ackTimeout,
			SubgroupSize:   1,
			Rng:            rand.New(rand.NewPCG(uint64(i), 0)),
			Notifier:       notifiers[i],
			HTTPClient:     &http.Client{Transport: &nodeTransport{rt: rt, hostAddr: network.conns[i].hostAddr}},
		}
		m, err := swim.New(cfg)
		require.NoError(t, err)
		members[i] = m
	}
	for i, m := range members {
		rt.register(fmt.Sprintf("node-%d:7947", i), m, network.conns[i].hostAddr)
	}

	return &cluster{
		t:              t,
		members:        members,
		network:        network,
		notifiers:      notifiers,
		protocolPeriod: protocolPeriod,
		ackTimeout:     ackTimeout,
	}
}

func (c *cluster) start(ctx context.Context) {
	c.t.Helper()
	for _, m := range c.members {
		go func() {
			if err := m.Start(ctx); err != nil {
				c.t.Errorf("Start: %v", err)
			}
		}()
	}
}

// partition drops node i from the network so it never receives or sends UDP packets,
// and blocks all HTTP so it cannot rejoin other nodes and other nodes cannot contact it.
func (c *cluster) partition(i int) {
	c.network.drop(i)
}

// partitionBetween blocks packets in both directions between nodes i and j,
// leaving each reachable from all other nodes.
func (c *cluster) partitionBetween(i, j int) {
	c.network.block(i, j)
	c.network.block(j, i)
}

func (c *cluster) addr(i int) string {
	return c.members[i].UDPAddr()
}

func (c *cluster) dead(i int) []string {
	return c.notifiers[i].dead()
}

// recordingNotifier records Notify calls for use in assertions.
type recordingNotifier struct {
	mu        sync.Mutex
	deadPeers []string
}

func (r *recordingNotifier) Notify(peer swim.Peer, kind swim.EventKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch kind {
	case swim.Dead:
		r.deadPeers = append(r.deadPeers, peer.UDPAddr())
	}
}

func (r *recordingNotifier) dead() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.deadPeers)
}

// network is an in-process packet network backed by channels. Each node gets a synthetic loopback
// address so that resolve can map host:port to *net.UDPAddr for routing. WriteTo delivers directly
// to the recipient's channel, making ReadFrom durably blocking within a [testing/synctest] bubble.
type network struct {
	mu       sync.Mutex
	conns    []*conn
	routes   map[string]chan packet  // keyed by raw "IP:port"
	hostToIP map[string]*net.UDPAddr // "host:port" -> *net.UDPAddr
	ipToHost map[string]string       // raw "IP:port" -> "host:port"
	blocked  map[[2]string]struct{}  // blocked[{from,to}] drops packets in that direction; keys are "host:port"
	isolated map[string]struct{}     // isolated["host:port"] drops all packets to/from that node
}

type packet struct {
	data []byte
	from *net.UDPAddr
}

func newNetwork(t *testing.T, hosts []string) *network {
	t.Helper()
	nodes := len(hosts)
	network := &network{
		conns:    make([]*conn, nodes),
		routes:   make(map[string]chan packet, nodes),
		hostToIP: make(map[string]*net.UDPAddr, nodes),
		ipToHost: make(map[string]string, nodes),
		blocked:  make(map[[2]string]struct{}),
		isolated: make(map[string]struct{}),
	}
	for i, host := range hosts {
		rawAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7946 + i}
		hostAddr := fmt.Sprintf("%s:%d", host, rawAddr.Port)
		// buffered so send never blocks: a blocked channel send is not durably blocked under synctest
		ch := make(chan packet, 16)
		network.routes[rawAddr.String()] = ch
		network.hostToIP[hostAddr] = rawAddr
		network.ipToHost[rawAddr.String()] = hostAddr
		network.conns[i] = &conn{network: network, addr: rawAddr, hostAddr: hostAddr, ch: ch}
	}
	return network
}

func (n *network) conn(i int) *conn {
	return n.conns[i]
}

// resolve maps a "host:port" peer address to its underlying *net.UDPAddr.
func (n *network) resolve(_ context.Context, addr string) (net.Addr, error) {
	n.mu.Lock()
	udpAddr, ok := n.hostToIP[addr]
	n.mu.Unlock()
	if !ok {
		return nil, &net.AddrError{Err: "unknown host", Addr: addr}
	}
	return udpAddr, nil
}

// drop fully isolates node i: it can neither send nor receive packets. The receive channel is
// closed to unblock any pending ReadFrom.
func (n *network) drop(i int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.conns[i]
	if ch, ok := n.routes[c.addr.String()]; ok {
		close(ch)
		delete(n.routes, c.addr.String())
	}
	n.isolated[c.hostAddr] = struct{}{}
}

func (n *network) dropAddr(rawAddr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ch, ok := n.routes[rawAddr]; ok {
		close(ch)
		delete(n.routes, rawAddr)
	}
}

// block silently drops packets from node i to node j.
func (n *network) block(i, j int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked[[2]string{n.conns[i].hostAddr, n.conns[j].hostAddr}] = struct{}{}
}

func (n *network) send(toRaw string, from *conn, data []byte) {
	n.mu.Lock()
	ch, ok := n.routes[toRaw]
	toHost := n.ipToHost[toRaw]
	_, isBlocked := n.blocked[[2]string{from.hostAddr, toHost}]
	_, isIsolated := n.isolated[from.hostAddr]
	n.mu.Unlock()
	if !ok || isBlocked || isIsolated {
		return // drop silently, like a real network
	}
	ch <- packet{data: data, from: from.addr}
}

// conn implements [net.PacketConn] using the network.
type conn struct {
	network  *network
	addr     *net.UDPAddr
	hostAddr string      // host:port, used for routing and blocking
	ch       chan packet // own receive channel, stored directly for durable blocking under synctest
	closed   atomic.Bool
}

func (c *conn) ReadFrom(b []byte) (int, net.Addr, error) {
	p, ok := <-c.ch
	if !ok {
		return 0, nil, net.ErrClosed
	}
	n := copy(b, p.data)
	return n, p.from, nil
}

func (c *conn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, &net.OpError{Op: "write", Err: net.InvalidAddrError("conn.WriteTo requires *net.UDPAddr")}
	}
	data := make([]byte, len(b))
	copy(data, b)
	c.network.send(udpAddr.String(), c, data)
	return len(b), nil
}

func (c *conn) Close() error {
	if c.closed.Swap(true) {
		return net.ErrClosed
	}
	c.network.dropAddr(c.addr.String())
	return nil
}

func (c *conn) LocalAddr() net.Addr              { return c.addr }
func (c *conn) SetDeadline(time.Time) error      { return nil }
func (c *conn) SetReadDeadline(time.Time) error  { return nil }
func (c *conn) SetWriteDeadline(time.Time) error { return nil }

type fakeListener struct {
	addr   *net.TCPAddr
	closed chan struct{}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *fakeListener) Addr() net.Addr { return l.addr }

func newFakeListener() net.Listener {
	return &fakeListener{
		addr:   net.TCPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 5000)),
		closed: make(chan struct{}),
	}
}
