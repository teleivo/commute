package swim_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
	"github.com/teleivo/commute/internal/swim"
)

func TestNew(t *testing.T) {
	network := newNetwork(t, []string{"7811223344aabb"})
	validConfig := swim.Config{
		NodeID:         "node-0",
		AdvertiseHost:  "7811223344aabb",
		Conn:           network.conn(0),
		Listener:       newFakeListener(),
		Resolve:        network.resolve,
		ProtocolPeriod: 1 * time.Second,
		AckTimeout:     500 * time.Millisecond,
		SubgroupSize:   3,
	}

	tests := map[string]struct {
		cfg     swim.Config
		wantErr bool
	}{
		"Valid": {
			cfg: validConfig,
		},
		"MissingNodeID": {
			cfg:     func() swim.Config { c := validConfig; c.NodeID = ""; return c }(),
			wantErr: true,
		},
		"MissingAdvertiseHost": {
			cfg:     func() swim.Config { c := validConfig; c.AdvertiseHost = ""; return c }(),
			wantErr: true,
		},
		"MissingConn": {
			cfg:     func() swim.Config { c := validConfig; c.Conn = nil; return c }(),
			wantErr: true,
		},
		"InvalidSeed": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = "notahost"; return c }(),
			wantErr: true,
		},
		"SeedMissingHost": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = ":7947"; return c }(),
			wantErr: true,
		},
		"SeedMissingPort": {
			cfg:     func() swim.Config { c := validConfig; c.Seeds = "127.0.0.1"; return c }(),
			wantErr: true,
		},
		"ZeroProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.ProtocolPeriod = 0; return c }(),
			wantErr: true,
		},
		"ZeroAckTimeout": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = 0; return c }(),
			wantErr: true,
		},
		"AckTimeoutEqualToProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = c.ProtocolPeriod; return c }(),
			wantErr: true,
		},
		"AckTimeoutGreaterThanProtocolPeriod": {
			cfg:     func() swim.Config { c := validConfig; c.AckTimeout = c.ProtocolPeriod + 1; return c }(),
			wantErr: true,
		},
		"ZeroSubgroupSize": {
			cfg:     func() swim.Config { c := validConfig; c.SubgroupSize = 0; return c }(),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := swim.New(tc.cfg)

			if tc.wantErr {
				assert.NotNil(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestProbeDirectSuccess verifies that a peer that replies to a ping is not declared dead.
func TestProbeDirectSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)
		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Round-robin: peer is selected within 2(n-1)=2 periods.
		time.Sleep(2 * c.protocolPeriod)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string(nil))
		cancel()
	})
}

// TestProbeIndirectSuccess verifies that a peer unreachable directly but reachable via an
// intermediary is not declared dead after indirect probing succeeds.
func TestProbeIndirectSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from node 0 but reachable from node 2.
		c := newCluster(t, 3)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partitionBetween(0, 1)

		// Round-robin: node 1 is selected within 2(n-1)=4 periods. Once probed, the direct ack
		// times out and the indirect ping via node 2 succeeds within the same period.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string(nil))
		assert.EqualValues(t, c.dead(2), []string(nil))
		cancel()
	})
}

// TestProbeIndirectFailPeerDead verifies that a peer unreachable both directly and via all
// intermediaries is declared dead after indirect probing fails.
func TestProbeIndirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from everyone so no path exists.
		c := newCluster(t, 3)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Round-robin: node 1 is selected within 2(n-1)=4 periods. Once probed, direct and indirect
		// pings both time out within the same period. Node 0 and node 2 run concurrently so both
		// converge within 4 periods.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string{c.addr(1)}, "node 1 must be declared dead by all nodes %v", []string{c.addr(0), c.addr(1), c.addr(2)})
		assert.EqualValues(t, c.dead(2), []string{c.addr(1)}, "node 1 must be declared dead by all nodes %v", []string{c.addr(0), c.addr(1), c.addr(2)})
		cancel()
	})
}

// TestProbeDirectFailPeerDead verifies that a peer that never replies is declared dead.
func TestProbeDirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		// Drop node 1 from the network so it never replies to pings.
		c.partition(1)

		// Round-robin: node 1 is selected within 2(n-1)=2 periods. The ack timeout and period
		// expiry both fall within the same period, so 2 periods is sufficient.
		time.Sleep(2 * c.protocolPeriod)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string{c.addr(1)}, "node 1 must be declared dead by node 0 %v", []string{c.addr(0), c.addr(1)})
		cancel()
	})
}

// TestProbeNoPeers verifies that the probe loop handles having no peers without hanging.
// This can happen in a 2-node cluster once the only peer is declared dead.
func TestProbeNoPeers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)

		ctx, cancel := context.WithCancel(t.Context())
		c.start(ctx)

		// Let bootstrap complete so the cluster is formed before the partition.
		synctest.Wait()
		c.partition(1)

		// Same detection bound as TestProbeDirectFailPeerDead, then extra periods to exercise the
		// probe loop with an empty peer list.
		time.Sleep(c.protocolPeriod * 4)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string{c.addr(1)}, "node 1 must be declared dead by node 0 %v", []string{c.addr(0), c.addr(1)})
		cancel()
	})
}

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

func (c *cluster) dead(i int) []string {
	return c.notifiers[i].dead()
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

// network is an in-process packet network backed by channels. Each node gets a real loopback
// address (127.0.0.1:N) so that net.ResolveUDPAddr in Probe resolves to the same key used to route
// packets. WriteTo delivers directly to the recipient's channel, making ReadFrom durably blocking
// within a [testing/synctest] bubble.
type network struct {
	mu       sync.Mutex
	conns    []*conn
	routes   map[string]chan packet  // keyed by raw "IP:port"
	hostToIP map[string]*net.UDPAddr // "node-N:port" -> *net.UDPAddr
	ipToHost map[string]string       // raw "IP:port" -> "node-N:port"
	blocked  map[[2]string]struct{}  // blocked[{from,to}] drops packets in that direction; keys are "node-N:port"
	isolated map[string]struct{}     // isolated["node-N:port"] drops all packets to/from that node
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

// resolve maps a "node-N:port" peer address to its underlying *net.UDPAddr.
func (n *network) resolve(_ context.Context, addr string) (net.Addr, error) {
	n.mu.Lock()
	udpAddr, ok := n.hostToIP[addr]
	n.mu.Unlock()
	if !ok {
		return nil, &net.AddrError{Err: "unknown host", Addr: addr}
	}
	return udpAddr, nil
}

// drop fully isolates node i: it can neither send nor receive packets.
// The receive channel is closed to unblock any pending ReadFrom.
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
