package swim_test

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
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
	network := newNetwork(t, 2)
	validConfig := swim.Config{
		NodeID:         "node-0",
		Conn:           network.conn(0),
		Peers:          network.addr(1),
		ProtocolPeriod: 1 * time.Second,
		AckTimeout:     500 * time.Millisecond,
		SubgroupSize:   3,
		Stderr:         io.Discard,
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
		"MissingConn": {
			cfg:     func() swim.Config { c := validConfig; c.Conn = nil; return c }(),
			wantErr: true,
		},
		"MissingPeers": {
			cfg:     func() swim.Config { c := validConfig; c.Peers = ""; return c }(),
			wantErr: true,
		},
		"InvalidPeer": {
			cfg:     func() swim.Config { c := validConfig; c.Peers = "notahost"; return c }(),
			wantErr: true,
		},
		"PeerMissingHost": {
			cfg:     func() swim.Config { c := validConfig; c.Peers = ":7947"; return c }(),
			wantErr: true,
		},
		"PeerMissingPort": {
			cfg:     func() swim.Config { c := validConfig; c.Peers = "127.0.0.1"; return c }(),
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
		c.start(t.Context())

		// Several protocol periods: node 0 pings node 1, node 1 replies every time.
		time.Sleep(3 * c.protocolPeriod)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string(nil))
	})
}

// TestProbeIndirectSuccess verifies that a peer unreachable directly but reachable via an
// intermediary is not declared dead after indirect probing succeeds.
func TestProbeIndirectSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from node 0 but reachable from node 2.
		c := newCluster(t, 3)
		c.partitionBetween(0, 1)

		c.start(t.Context())

		// Wait past the direct ack timeout and the indirect ack timeout so ping-req
		// has had time to complete, but node 1 should survive via node 2.
		time.Sleep(c.protocolPeriod + c.ackTimeout*2)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string(nil))
		assert.EqualValues(t, c.dead(2), []string(nil))
	})
}

// TODO tests are flak y

// TestProbeIndirectFailPeerDead verifies that a peer unreachable both directly and via all
// intermediaries is declared dead after indirect probing fails.
func TestProbeIndirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 3 nodes: node 0 probes node 1, node 2 is the intermediary.
		// node 1 is partitioned from everyone so no path exists.
		c := newCluster(t, 3)
		c.partition(1)

		c.start(t.Context())

		// Wait past both the direct and indirect ack timeouts.
		time.Sleep(c.protocolPeriod + c.ackTimeout*2)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string{c.addr(1)})
		// assert.EqualValues(t, c.dead(2), []string{c.addr(1)})
	})
}

// TestProbeDirectFailPeerDead verifies that a peer that never replies is declared dead.
func TestProbeDirectFailPeerDead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t, 2)
		// Drop node 1 from the network so it never replies to pings.
		c.partition(1)

		c.start(t.Context())

		// Wait past the ack timeout so node 0 declares node 1 dead.
		time.Sleep(c.protocolPeriod + c.ackTimeout)
		synctest.Wait()

		assert.EqualValues(t, c.dead(0), []string{c.addr(1)})
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
	network := newNetwork(t, nodes)
	members := make([]*swim.Member, nodes)
	for i := range nodes {
		peers := make([]string, 0, nodes-1)
		for j := range nodes {
			if i != j {
				peers = append(peers, network.addr(j))
			}
		}
		cfg := swim.Config{
			NodeID:         fmt.Sprintf("node-%d", i),
			Conn:           network.conn(i),
			Peers:          strings.Join(peers, ","),
			ProtocolPeriod: protocolPeriod,
			AckTimeout:     ackTimeout,
			SubgroupSize:   1,
			Rng:            rand.New(rand.NewPCG(uint64(i), 0)),
			Notifier:       notifiers[i],
			Stderr:         io.Discard,
		}
		m, err := swim.New(cfg)
		require.NoError(t, err)
		members[i] = m
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

// partition drops node i from the network so it never receives or sends packets.
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
	return c.network.addr(i)
}

// recordingNotifier records Notify calls for use in assertions.
type recordingNotifier struct {
	mu        sync.Mutex
	deadPeers []string
}

func (r *recordingNotifier) Notify(peer string, kind swim.EventKind) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if kind == swim.Dead {
		r.deadPeers = append(r.deadPeers, peer)
	}
}

func (r *recordingNotifier) dead() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deadPeers
}

// network is an in-process packet network backed by channels.
// Each node gets a real loopback address (127.0.0.1:N) so that
// net.ResolveUDPAddr in Probe resolves to the same key used to route
// packets. WriteTo delivers directly to the recipient's channel, making
// ReadFrom durably blocking within a [testing/synctest] bubble.
type network struct {
	mu      sync.Mutex
	conns   []*conn
	routes  map[string]chan packet
	blocked map[[2]string]struct{} // blocked[{from,to}] drops packets in that direction
}

type packet struct {
	data []byte
	from *net.UDPAddr
}

func newNetwork(t *testing.T, nodes int) *network {
	t.Helper()
	network := &network{
		conns:   make([]*conn, nodes),
		routes:  make(map[string]chan packet, nodes),
		blocked: make(map[[2]string]struct{}),
	}
	for i := range nodes {
		udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		require.NoError(t, err)
		// Listen then immediately close to grab a free port from the OS.
		l, err := net.ListenUDP("udp", udpAddr)
		require.NoError(t, err)
		addr := l.LocalAddr().(*net.UDPAddr)
		require.NoError(t, l.Close())
		network.routes[addr.String()] = make(chan packet, 16)
		network.conns[i] = &conn{network: network, addr: addr}
	}
	return network
}

func (n *network) conn(i int) *conn {
	return n.conns[i]
}

func (n *network) addr(i int) string {
	return n.conns[i].addr.String()
}

// drop closes and removes node i's routes, unblocking any pending ReadFrom.
func (n *network) drop(i int) {
	n.dropAddr(n.addr(i))
}

func (n *network) dropAddr(addr string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ch, ok := n.routes[addr]; ok {
		close(ch)
		delete(n.routes, addr)
	}
}

// block silently drops packets from node i to node j.
func (n *network) block(i, j int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked[[2]string{n.addr(i), n.addr(j)}] = struct{}{}
}

func (n *network) send(to string, from *net.UDPAddr, data []byte) {
	n.mu.Lock()
	ch, ok := n.routes[to]
	_, isBlocked := n.blocked[[2]string{from.String(), to}]
	n.mu.Unlock()
	if !ok || isBlocked {
		return // drop silently, like a real network
	}
	ch <- packet{data: data, from: from}
}

// conn implements [net.PacketConn] using the network.
type conn struct {
	network *network
	addr    *net.UDPAddr
	closed  atomic.Bool
}

func (c *conn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.closed.Load() {
		return 0, nil, net.ErrClosed
	}
	c.network.mu.Lock()
	ch, ok := c.network.routes[c.addr.String()]
	c.network.mu.Unlock()
	if !ok {
		return 0, nil, net.ErrClosed
	}
	p, ok := <-ch
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
	c.network.send(udpAddr.String(), c.addr, data)
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
