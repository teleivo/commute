// Package swim implements the SWIM failure detector as described in [SWIM: Scalable
// Weakly-consistent Infection-style Process Group Membership Protocol], Section 4.
//
// [SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol]: https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf
package swim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Member is a node participating in the SWIM failure detection protocol.
type Member struct {
	logger         *slog.Logger
	nodeID         string
	conn           net.PacketConn
	peers          []string
	peerAddrs      map[string]net.Addr // pre-resolved at New time; guarded by muPeers
	muPeers        sync.RWMutex
	protocolPeriod time.Duration
	ackTimeout     time.Duration
	period         atomic.Uint64
	subgroupSize   int
	rng            *rand.Rand
	acks           chan Ack
	notifier       Notifier
}

// Config holds the configuration for creating a Member.
type Config struct {
	NodeID         string
	Conn           net.PacketConn                      // UDP connection to receive and send packets on
	Peers          string                              // comma-separated list of peer addresses (e.g. host1:7946,host2:7946)
	Resolve        func(addr string) (net.Addr, error) // if nil, defaults to net.ResolveUDPAddr
	ProtocolPeriod time.Duration                       // T' in the paper: duration of one failure detection round
	AckTimeout     time.Duration                       // how long to wait for a direct ack before declaring a peer dead
	SubgroupSize   int                                 // k in the paper: number of members used for indirect probing
	Notifier       Notifier                            // if nil, membership changes are not reported
	Rng            *rand.Rand                          // random source for peer selection
	Logger         *slog.Logger                        // if nil, logging is disabled
}

// New creates a Member from the given Config.
func New(cfg Config) (*Member, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	if cfg.Conn == nil {
		return nil, errors.New("conn is required")
	}
	if cfg.Peers == "" {
		return nil, errors.New("at least one peer is required")
	}
	resolve := cfg.Resolve
	if resolve == nil {
		resolve = func(addr string) (net.Addr, error) {
			return net.ResolveUDPAddr("udp", addr)
		}
	}
	peers := make(map[string]struct{})
	peerAddrs := make(map[string]net.Addr)
	for p := range strings.SplitSeq(cfg.Peers, ",") {
		p = strings.TrimSpace(p)
		host, port, err := net.SplitHostPort(p)
		if err != nil {
			return nil, fmt.Errorf("invalid peer %q: %s", p, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("invalid peer %q: host and port are required", p)
		}
		addr, err := resolve(p)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve peer %q: %w", p, err)
		}
		peers[p] = struct{}{}
		peerAddrs[p] = addr
	}
	if cfg.ProtocolPeriod <= 0 {
		return nil, errors.New("protocol period must be greater than zero")
	}
	if cfg.AckTimeout <= 0 {
		return nil, errors.New("ack timeout must be greater than zero")
	}
	if cfg.AckTimeout >= cfg.ProtocolPeriod {
		return nil, errors.New("ack timeout must be less than protocol period")
	}
	if cfg.SubgroupSize <= 0 {
		return nil, errors.New("subgroup size must be greater than zero")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With(
		slog.String("component", "swim"),
		slog.String("node_id", cfg.NodeID),
	)
	rng := cfg.Rng
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	member := &Member{
		logger:         logger,
		nodeID:         cfg.NodeID,
		conn:           cfg.Conn,
		peers:          slices.Sorted(maps.Keys(peers)),
		peerAddrs:      peerAddrs,
		protocolPeriod: cfg.ProtocolPeriod,
		ackTimeout:     cfg.AckTimeout,
		subgroupSize:   cfg.SubgroupSize,
		rng:            rng,
		acks:           make(chan Ack, 1), // shared across rounds; a stale ack can evict a live one via the non-blocking send in Listen
		notifier:       cfg.Notifier,
	}
	return member, nil
}

// Start runs the Listen and Probe loops until ctx is cancelled.
func (m *Member) Start(ctx context.Context) error {
	m.logger.Info("listening", "addr", m.conn.LocalAddr())

	var wg sync.WaitGroup
	wg.Go(func() {
		m.Listen(ctx)
	})
	wg.Go(func() {
		m.Probe(ctx)
	})
	wg.Go(func() {
		<-ctx.Done()
		if err := m.conn.Close(); err != nil {
			m.logger.Error("failed to close connection", "error", err)
		}
	})
	wg.Wait()
	return nil
}

// Ack is an acknowledgement received from a peer.
type Ack struct {
	Period uint64
	Addr   string
}

// relayKey identifies a pending relay ack for a ping-req. Target is the address
// of the node being pinged on behalf of the requester; Period is the initiator's
// protocol period echoed in the ack. A struct key avoids ambiguity from string
// concatenation (e.g. "1.2.3.4:56"+"78" vs "1.2.3.4:567"+"8").
type relayKey struct {
	target string
	period uint64
}

// Listen reads incoming UDP messages and dispatches them: acks are forwarded to
// the Probe loop; pings are answered immediately; ping-reqs are relayed to the target.
func (m *Member) Listen(ctx context.Context) {
	relayAcks := make(map[relayKey]chan Ack)
	var mu sync.RWMutex

	for {
		b := make([]byte, maxMessageSize)
		n, addr, err := m.conn.ReadFrom(b)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			m.logger.Error("failed to read UDP message", "error", err)
			continue
		}
		var msg Message
		if err := msg.UnmarshalBinary(b[:n]); err != nil {
			m.logger.Error("failed to parse message", "addr", addr, "error", err)
			continue
		}

		m.logger.Debug("got message", "addr", addr, "kind", msg.Kind, "period", msg.Period, "target", msg.Target)
		switch msg.Kind {
		case ack:
			// relay ack carries the original probe target in Target so we can route it
			// to the right relay waiter and deliver an Ack as if it came from the target
			ackAddr := addr.String()
			if msg.TargetLen > 0 {
				ackAddr = string(msg.Target)
			}

			ackCh := m.acks
			mu.RLock()
			if ch, ok := relayAcks[relayKey{target: ackAddr, period: msg.Period}]; ok {
				ackCh = ch
			}
			mu.RUnlock()

			// non-blocking send: default is only taken when no other case can proceed, so the ack
			// lands in the buffer if there is room and is dropped if the buffer is full (e.g. a
			// stale ack is waiting). A dropped ack is harmless as the probe loop will fall back to
			// indirect probing via ping-req on timeout.
			select {
			case ackCh <- Ack{Period: msg.Period, Addr: ackAddr}:
			default:
			}
		case ping:
			reply := NewMessage(ack, msg.Period, "")
			_ = m.sendToAddr(addr.String(), addr, reply)
		case pingReq:
			if msg.TargetLen == 0 {
				m.logger.Warn("message is missing required target for indirect ping", "addr", addr)
				continue
			}

			key := relayKey{target: string(msg.Target), period: msg.Period}
			ackCh := make(chan Ack, 1)
			mu.Lock()
			relayAcks[key] = ackCh
			mu.Unlock()

			go func(acks <-chan Ack, done func()) {
				defer done()

				target := string(msg.Target)
				if err := m.send(target, NewMessage(ping, msg.Period, "")); err != nil {
					return
				}

				ackTimeout := time.NewTimer(m.ackTimeout)
			waitAck:
				for {
					select {
					case <-ackTimeout.C:
						break waitAck
					case a := <-acks:
						if a.Period == msg.Period && a.Addr == target {
							ackTimeout.Stop()
							// carry the target in the relay ack so the requester can route it and
							// distinguish from a ping it might have sent to the target itself
							_ = m.sendToAddr(addr.String(), addr, NewMessage(ack, msg.Period, target))
							break waitAck
						}
					}
				}
			}(ackCh, func() {
				mu.Lock()
				close(ackCh)
				delete(relayAcks, key)
				mu.Unlock()
			})
		}
	}
}

// EventKind identifies the type of membership change a [Notifier] receives.
type EventKind uint8

const (
	Dead EventKind = iota
	Alive
)

func (e EventKind) String() string {
	switch e {
	case Dead:
		return "dead"
	case Alive:
		return "alive"
	default:
		panic(fmt.Sprintf("unknown EventKind %d", uint8(e)))
	}
}

// Notifier is called by a Member when a peer's membership status changes. Notify is called in a
// goroutine so implementations may block without affecting the probe loop.
type Notifier interface {
	Notify(peer string, kind EventKind)
}

// Probe runs the failure detection loop: once per protocol period it picks a
// random peer, sends a ping, and waits up to AckTimeout for a direct ack before
// declaring the peer dead and removing it from the peer list.
func (m *Member) Probe(ctx context.Context) {
	periodTimer := time.NewTimer(m.protocolPeriod)
	defer periodTimer.Stop()
	ackTimeout := time.NewTimer(m.ackTimeout)
	defer ackTimeout.Stop()

	for {
		if ctx.Err() != nil {
			return
		}

		periodTimer.Reset(m.protocolPeriod)
		period := m.period.Add(1)

		m.muPeers.RLock()
		if len(m.peers) == 0 {
			m.muPeers.RUnlock()
			m.logger.Warn("no peers to probe")
			select {
			case <-ctx.Done():
				return
			case <-periodTimer.C:
			}
			continue
		}
		peer := m.randomPeer()
		m.muPeers.RUnlock()

		// direct ping
		if err := m.send(peer, NewMessage(ping, period, "")); err != nil {
			select {
			case <-ctx.Done():
				return
			case <-periodTimer.C:
			}
			continue
		}

		ackTimeout.Reset(m.ackTimeout)
	waitAck:
		for {
			select {
			case <-ctx.Done():
				return
			case <-ackTimeout.C:
				// indirect ping-req as we did not receive an ack to direct ping on time
				// TODO: avoid copying peers on every round; snapshot prevents infinite loop if peers shrinks to 1 concurrently.
				m.muPeers.RLock()
				candidates := make([]string, 0, len(m.peers))
				for _, p := range m.peers {
					if p != peer {
						candidates = append(candidates, p)
					}
				}
				m.muPeers.RUnlock()
				if len(candidates) == 0 {
					continue
				}

				for range m.subgroupSize {
					indirect := candidates[m.rng.IntN(len(candidates))]
					_ = m.send(indirect, NewMessage(pingReq, period, peer))
				}
			case <-periodTimer.C:
				// period ended without getting an ack so peer is declared dead
				ackTimeout.Stop()
				m.muPeers.Lock()
				m.peers = slices.DeleteFunc(m.peers, func(p string) bool { return p == peer })
				delete(m.peerAddrs, peer)
				m.muPeers.Unlock()
				m.logger.Info("peer is dead", "peer", peer, "period", period)
				if m.notifier != nil {
					go m.notifier.Notify(peer, Dead)
				}
				break waitAck
			case a := <-m.acks:
				if a.Period == period && a.Addr == peer {
					m.logger.Debug("peer is alive", "peer", peer, "period", period)
					ackTimeout.Stop()
					// wait for the period to expire before moving on to the next probe
					select {
					case <-ctx.Done():
						return
					case <-periodTimer.C:
					}
					break waitAck
				}
			}
		}
	}
}

func (m *Member) randomPeer() string {
	return m.peers[m.rng.IntN(len(m.peers))]
}

// send delivers msg to peer using the pre-resolved address from New.
func (m *Member) send(peer string, msg Message) error {
	m.muPeers.RLock()
	addr := m.peerAddrs[peer]
	m.muPeers.RUnlock()
	if addr == nil {
		m.logger.Error("no resolved address for peer", "peer", peer)
		return fmt.Errorf("no resolved address for peer %q", peer)
	}
	return m.sendToAddr(peer, addr, msg)
}

func (m *Member) sendToAddr(peer string, addr net.Addr, msg Message) error {
	b, err := msg.MarshalBinary()
	if err != nil {
		panic(err)
	}
	_, err = m.conn.WriteTo(b, addr)
	if err != nil {
		m.logger.Error("failed to send message", "peer", peer, "kind", msg.Kind, "period", msg.Period, "target", msg.Target, "error", err)
		return err
	}
	m.logger.Debug("sent message", "peer", peer, "kind", msg.Kind, "period", msg.Period, "target", msg.Target)
	return nil
}
