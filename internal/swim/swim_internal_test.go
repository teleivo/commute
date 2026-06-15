package swim

import (
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/teleivo/assertive/assert"
	"github.com/teleivo/assertive/require"
)

func TestEventQueuePopEmpty(t *testing.T) {
	var q EventQueue

	got := q.Pop(1)

	assert.Nil(t, got)
}

func TestEventQueuePopMoreThanAvailable(t *testing.T) {
	var q EventQueue
	e := EventItem{Event: Event{Kind: Dead, Node: "192.168.1.1:7946"}}
	q.Push(e)

	got := q.Pop(5)

	assertEventItems(t, []EventItem{e}, got)
}

func TestEventQueuePopReturnsLeastSent(t *testing.T) {
	tests := map[string]struct {
		events []Event
	}{
		"TwoEvents": {
			events: []Event{
				{Kind: Dead, Node: "192.168.1.1:7946"},
				{Kind: Alive, Node: "192.168.1.2:7946"},
			},
		},
		"ThreeEvents": {
			events: []Event{
				{Kind: Dead, Node: "192.168.1.1:7946"},
				{Kind: Alive, Node: "192.168.1.2:7946"},
				{Kind: Dead, Node: "192.168.1.3:7946"},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var q EventQueue
			for _, e := range tc.events {
				q.Push(EventItem{Event: e})
			}
			first := q.Pop(1)
			assertEventItems(t, []EventItem{{Event: tc.events[0]}}, first)
			first[0].SendCount++
			q.Push(first[0])

			second := q.Pop(1)

			require.EqualValues(t, 1, len(second))
			assert.EqualValues(t, false, second[0].Event == first[0].Event && second[0].SendCount == first[0].SendCount)
		})
	}
}

// TestSendToAddrEventRequeuedOnFailure verifies that events popped from the queue before a failed
// send are re-enqueued so they are not silently lost.
func TestSendToAddrEventRequeuedOnFailure(t *testing.T) {
	// A closed conn makes WriteTo return an error, simulating a send failure.
	closed := &closedConn{
		addr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 5000)),
	}

	m := &Member{
		logger:              slog.New(slog.DiscardHandler),
		conn:                closed,
		disseminationFactor: 2,
		// one peer keeps log2(len(peers)+1) > 0 so events survive re-enqueue
		peers: []string{"127.0.0.1:9999"},
	}
	e := EventItem{Event: Event{Kind: Dead, Node: "192.168.1.1:7946"}}
	m.eventQueue.Push(e)

	_ = m.sendToAddr("127.0.0.1:9999", closed.addr, NewMessage(ping, "node-0:7946", 1, ""))

	got := m.eventQueue.Pop(1)
	require.EqualValues(t, 1, len(got), "event was lost on failed send but should have been re-queued")
	assert.EqualValues(t, e.Event, got[0].Event)
	assert.EqualValues(t, 0, got[0].SendCount, "send count should not be incremented on failed send")
}

// TestSendToAddrEventDissemination verifies that on a successful send the event's send count is
// incremented and that it is dropped from the queue once the dissemination threshold is reached.
func TestSendToAddrEventDissemination(t *testing.T) {
	discard := &discardConn{
		addr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), 5000)),
	}

	m := &Member{
		logger:              slog.New(slog.DiscardHandler),
		conn:                discard,
		disseminationFactor: 2,
		peers:               []string{"127.0.0.1:9001", "127.0.0.1:9002"},
	}
	const maxDisseminations = 4 // ceil(2 x log2(2+1)) = ceil(2 x 1.58) = 4

	m.eventQueue.Push(EventItem{Event: Event{Kind: Dead, Node: "192.168.1.1:7946"}})

	for i := range maxDisseminations {
		_ = m.sendToAddr("127.0.0.1:9001", discard.addr, NewMessage(ping, "node-0:7946", uint64(i), ""))

		got := m.eventQueue.Pop(1)
		if i < maxDisseminations-1 {
			require.EqualValues(t, 1, len(got), "send %d: event should still be in queue", i+1)
			assert.EqualValues(t, i+1, got[0].SendCount, "send %d: send count should be incremented", i+1)
			m.eventQueue.Push(got[0])
		} else {
			assert.Nil(t, got, "event should be dropped after %d disseminations", maxDisseminations)
		}
	}
}

// TestKRandomPeers verifies peer selection for indirect probing.
func TestKRandomPeers(t *testing.T) {
	tests := map[string]struct {
		peers        []string
		nodeID       string
		subgroupSize int
		target       string
		wantNone     bool
		wantCount    int
	}{
		"NoCandidatesOnlyTargetInPeers": {
			peers:        []string{"node-1:7946"},
			nodeID:       "node-0",
			subgroupSize: 1,
			target:       "node-1:7946",
			wantNone:     true,
		},
		"NoCandidatesEmpty": {
			peers:        []string{},
			nodeID:       "node-0",
			subgroupSize: 1,
			target:       "node-1:7946",
			wantNone:     true,
		},
		"CandidatesLessThanSubgroupSize": {
			// 2 candidates available but subgroupSize=3: should return both candidates
			peers:        []string{"node-1:7946", "node-2:7946", "node-3:7946"},
			nodeID:       "node-0",
			subgroupSize: 3,
			target:       "node-1:7946",
			wantCount:    2, // node-2 and node-3; node-1 is the target
		},
		"CandidatesEqualSubgroupSize": {
			// 2 candidates available and subgroupSize=2: should return both candidates
			peers:        []string{"node-1:7946", "node-2:7946", "node-3:7946"},
			nodeID:       "node-0",
			subgroupSize: 2,
			target:       "node-1:7946",
			wantCount:    2, // node-2 and node-3; node-1 is the target
		},
		"CandidatesGreaterThanSubgroupSize": {
			// 3 candidates available but subgroupSize=2: should return exactly k peers
			peers:        []string{"node-1:7946", "node-2:7946", "node-3:7946", "node-4:7946"},
			nodeID:       "node-0",
			subgroupSize: 2,
			target:       "node-1:7946",
			wantCount:    2,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m := &Member{
				nodeID:       tc.nodeID,
				peers:        tc.peers,
				subgroupSize: tc.subgroupSize,
				rng:          rand.New(rand.NewPCG(42, 0)),
			}

			got, ok := m.kRandomPeers(tc.target)

			if tc.wantNone {
				assert.False(t, ok)
				assert.Nil(t, got)
				return
			}

			assert.True(t, ok)
			assert.EqualValues(t, tc.wantCount, len(got))

			seen := make(map[string]struct{}, len(got))
			for _, p := range got {
				// target and self must never appear in the result
				assert.False(t, p == tc.target, "target %q must not be selected as intermediary", tc.target)
				assert.False(t, p == tc.nodeID, "self %q must not be selected as intermediary", tc.nodeID)

				// all returned peers must be known peers
				assert.True(t, slices.Contains(tc.peers, p), "selected peer %q is not in the known peer list", p)

				_, duplicate := seen[p]
				assert.False(t, duplicate, "peer %q selected more than once", p)
				seen[p] = struct{}{}
			}
		})
	}
}

func assertEventItems(t *testing.T, want, got []EventItem) {
	t.Helper()
	require.EqualValues(t, len(want), len(got), "want %v got %v", want, got)
	for i := range want {
		assert.EqualValues(t, want[i].Event, got[i].Event, "item %d: want %v got %v", i, want[i], got[i])
		assert.EqualValues(t, want[i].SendCount, got[i].SendCount, "item %d: want %v got %v", i, want[i], got[i])
	}
}

// closedConn is a net.PacketConn whose WriteTo always fails.
type closedConn struct {
	addr *net.UDPAddr
}

func (c *closedConn) ReadFrom(b []byte) (int, net.Addr, error)     { return 0, nil, net.ErrClosed }
func (c *closedConn) WriteTo(b []byte, addr net.Addr) (int, error) { return 0, net.ErrClosed }
func (c *closedConn) Close() error                                 { return nil }
func (c *closedConn) LocalAddr() net.Addr                          { return c.addr }
func (c *closedConn) SetDeadline(_ time.Time) error                { return nil }
func (c *closedConn) SetReadDeadline(_ time.Time) error            { return nil }
func (c *closedConn) SetWriteDeadline(_ time.Time) error           { return nil }

// discardConn is a net.PacketConn whose WriteTo always succeeds.
type discardConn struct {
	addr *net.UDPAddr
}

func (c *discardConn) ReadFrom(b []byte) (int, net.Addr, error)     { return 0, nil, net.ErrClosed }
func (c *discardConn) WriteTo(b []byte, addr net.Addr) (int, error) { return len(b), nil }
func (c *discardConn) Close() error                                 { return nil }
func (c *discardConn) LocalAddr() net.Addr                          { return c.addr }
func (c *discardConn) SetDeadline(_ time.Time) error                { return nil }
func (c *discardConn) SetReadDeadline(_ time.Time) error            { return nil }
func (c *discardConn) SetWriteDeadline(_ time.Time) error           { return nil }
