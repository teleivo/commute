// Package swim implements the SWIM failure detector as described in [SWIM: Scalable
// Weakly-consistent Infection-style Process Group Membership Protocol], Section 3.1.
//
// [SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol]: https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf
package swim

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	muPeers        sync.RWMutex
	protocolPeriod time.Duration
	ackTimeout     time.Duration
	period         atomic.Uint64
	subgroupSize   int
	rng            *rand.Rand
	acks           chan Ack
	notifier       Notifier
}

// TODO now that we use net.PacketConn could this be a non udp impl?
// Config holds the configuration for creating a Member.
type Config struct {
	NodeID         string
	Conn           net.PacketConn                      // connection to receive and send packets on
	Peers          string                              // comma-separated list of peer addresses (e.g. host1:7946,host2:7946)
	ProtocolPeriod time.Duration                       // T' in the paper: duration of one failure detection round; must be at least 3× the estimated round-trip time
	AckTimeout     time.Duration                       // how long to wait for a direct ack before declaring a peer dead; must be less than ProtocolPeriod
	SubgroupSize   int                                 // k in the paper: number of members used for indirect probing
	Notifier       Notifier                            // optional; called when a peer's membership status changes
	Resolve        func(addr string) (net.Addr, error) // optional; resolves a peer address string to a net.Addr; defaults to net.ResolveUDPAddr
	Rng            *rand.Rand                          // random source for peer selection
	Debug          bool                                // enable debug logging
	Stderr         io.Writer                           // output for error logging
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
	peers := make(map[string]struct{})
	for p := range strings.SplitSeq(cfg.Peers, ",") {
		p = strings.TrimSpace(p)
		host, port, err := net.SplitHostPort(p)
		if err != nil {
			return nil, fmt.Errorf("invalid peer %q: %s", p, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("invalid peer %q: host and port are required", p)
		}
		peers[p] = struct{}{}
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

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(cfg.Stderr, &slog.HandlerOptions{Level: level}))
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
		protocolPeriod: cfg.ProtocolPeriod,
		ackTimeout:     cfg.AckTimeout,
		subgroupSize:   cfg.SubgroupSize,
		rng:            rng,
		acks:           make(chan Ack, 1),
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
	Addr   net.Addr
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
// the Probe loop; pings are answered immediately; ping-reqs are not yet handled.
func (m *Member) Listen(ctx context.Context) {
	relayAcks := make(map[relayKey]chan Ack)
	var mu sync.RWMutex

	for {
		// TODO what if there are more bytes to be read than our max?
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
			// Non-blocking send: default is only taken when no other case can proceed, so the
			// ack lands in the buffer if there is room and is dropped if the buffer is full
			// (e.g. a stale ack is waiting). A dropped ack is harmless; the probe loop will
			// fall back to indirect probing via ping-req on timeout.

			c := m.acks
			mu.RLock()
			if d, ok := relayAcks[relayKey{target: addr.String(), period: msg.Period}]; ok {
				c = d
			}
			mu.RUnlock()

			select {
			case c <- Ack{Period: msg.Period, Addr: addr}:
			default:
			}
		case ping:
			reply := NewMessage(ack, msg.Period, "")
			m.sendToAddr(addr.String(), addr, reply) //nolint:errcheck
		case pingReq:
			if msg.TargetLen == 0 {
				m.logger.Warn("message is missing required target for indirect ping", "addr", addr, "error", err)
				continue
			}

			key := relayKey{target: string(msg.Target), period: msg.Period}
			c := make(chan Ack, 1)
			mu.Lock()
			relayAcks[key] = c
			mu.Unlock()

			go func(acks <-chan Ack, done func()) {
				defer done()

				target := string(msg.Target)
				// TODO should we log that we make this ping due to pingReq? or clear due to
				m.logger.Debug("relay pingReq", "target", target)
				// previous debug log?
				ping := NewMessage(ping, msg.Period, "")
				if err := m.send(target, ping); err != nil {
					return
				}

				// TODO
				// If this is not received within a prespecified time-out
				// (determined by the message round-trip time, which is cho-
				// sen smaller than the protocol period), indirectly probes .
				// selects members at random and sends each a
				// ping-req( ) message. Each of these members in turn
				// (those that are non-faulty), on receiving this message, pings
				// and forwards the ack from
				// (if received) back to
				// . At the end of this protocol period,
				// checks if it has
				// received any acks, directly from
				// or indirectly through
				// one of the members; if not, it declares
				// as failed in
				// its local membership list, and hands this update off to the
				// Dissemination Component.

				ackTimeout := time.NewTimer(m.ackTimeout)
				ackTimeout.Reset(m.ackTimeout)
			waitAck:
				for {
					select {
					case <-ctx.Done():
						ackTimeout.Stop()
						return
					case <-ackTimeout.C:
						// TODO anything we should do here? other than break, log?
						break waitAck
					case a := <-acks:
						if a.Period == msg.Period && a.Addr.String() == target {
							// TODO log something like this?
							// m.logger.Debug("peer is alive", "peer", peer, "period", period)
							ackTimeout.Stop()
							ackMsg := NewMessage(ack, msg.Period, "")
							err := m.sendToAddr(addr.String(), addr, ackMsg)
							if err != nil {
								continue
							}
							break waitAck
						}
					}
				}
			}(c, func() {
				mu.Lock()
				close(c)
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

// Notifier is called by a Member when a peer's membership status changes.
type Notifier interface {
	Notify(peer string, kind EventKind)
}

// Probe runs the failure detection loop: once per protocol period it picks a
// random peer, sends a ping, and waits up to AckTimeout for a direct ack before
// declaring the peer dead and removing it from the peer list.
func (m *Member) Probe(ctx context.Context) {
	// TODO revisit if I can make this state-machine easier by adding state on Member? or creating a
	// probe type with that state
	ackTimeout := time.NewTimer(m.ackTimeout)
	for {
		errPeriodEnded := errors.New("period ended")
		periodCtx, periodCtxCancel := context.WithTimeoutCause(ctx, m.protocolPeriod, errPeriodEnded)

		period := m.period.Add(1)
		m.muPeers.RLock() // TODO right now we don't actually need a lock on peers but guess its safer for future? or only add when needed
		if len(m.peers) == 0 {
			m.muPeers.RUnlock()
			m.logger.Warn("no peers to probe")
			periodCtxCancel()
			continue
		}
		peer := m.peer()
		m.muPeers.RUnlock()

		// direct ping
		msg := NewMessage(ping, period, "")
		// TODO is this blocking? can I make a send taking a context that times out when send blocks
		// to long?
		if err := m.send(peer, msg); err != nil {
			// TODO without a ticker we need to wait the periodCtx so we don't probe more frequent
			<-periodCtx.Done()
			periodCtxCancel()
			continue
		}

		// TODO
		// If this is not received within a prespecified time-out
		// (determined by the message round-trip time, which is cho-
		// sen smaller than the protocol period), indirectly probes .
		// selects members at random and sends each a
		// ping-req( ) message. Each of these members in turn
		// (those that are non-faulty), on receiving this message, pings
		// and forwards the ack from
		// (if received) back to
		// . At the end of this protocol period,
		// checks if it has
		// received any acks, directly from
		// or indirectly through
		// one of the members; if not, it declares
		// as failed in
		// its local membership list, and hands this update off to the
		// Dissemination Component.

		ackTimeout.Reset(m.ackTimeout)
	waitAck:
		for {
			select {
			case <-periodCtx.Done():
				// TODO deal with ended period here? but I only wanted to react to parent
				periodCtxCancel()
				ackTimeout.Stop()
				if context.Cause(periodCtx) != errPeriodEnded {
					return
				}
			case <-ackTimeout.C:
				fmt.Println("foo")
				// indirect ping
				// for k := 0; k < m.subgroupSize; {
				for range m.subgroupSize {
					fmt.Println("faa")
					indirect := m.peer() // TODO better name for indirect?
					if indirect == peer {
						continue
					}
					// TODO keep as is for now with kind and optional target? better name than
					// target like suspect?
					msg := NewMessage(pingReq, period, peer)
					// TODO send already deals with error as best as we can no?
					_ = m.send(indirect, msg)
				}

				for {
					select {
					case <-periodCtx.Done():
						periodCtxCancel()
						if context.Cause(periodCtx) != errPeriodEnded {
							return
						}

						m.muPeers.Lock()
						m.peers = slices.DeleteFunc(m.peers, func(p string) bool {
							return p == peer
						})
						m.muPeers.Unlock()
						m.logger.Info("peer is dead", "peer", peer, "period", period)
						// TODO call in separate go routine as this blocks Probe loop?
						if m.notifier != nil {
							m.notifier.Notify(peer, Dead)
						}
						break waitAck
					case a := <-m.acks:
						// TODO what are they sending back? is the ping-req Ack given a different
						// ack?
						if a.Period == period && a.Addr.String() == peer {
							m.logger.Debug("peer is alive", "peer", peer, "period", period)
							periodCtxCancel()
							break waitAck
						}
					}
				}
			case a := <-m.acks:
				if a.Period == period && a.Addr.String() == peer {
					m.logger.Debug("peer is alive", "peer", peer, "period", period)
					ackTimeout.Stop()
					periodCtxCancel()
					break waitAck
				}
			}
		}
	}
}

func (m *Member) peer() string {
	return m.peers[m.rng.IntN(len(m.peers))]
}

// TODO add that this will resolve peer in contrast to sendToAddr
// send
func (m *Member) send(peer string, msg Message) error {
	// TODO better to create logger := m.logger.With with all fields or do as I currently do? what
	// is more costly vs what is more readable?
	addr, err := net.ResolveUDPAddr(m.conn.LocalAddr().Network(), peer)
	if err != nil {
		m.logger.Error("failed to resolve peer address", "peer", peer, "kind", msg.Kind, "period", msg.Period, "target", msg.Target, "error", err)
		// TODO or not return an error but a bool? as I logged so this is handled?
		return err
	}
	return m.sendToAddr(peer, addr, msg)
}

func (m *Member) sendToAddr(peer string, addr net.Addr, msg Message) error {
	// TODO should this not get the peer and be more "low-level". we could pass in a logger so we
	// can potentially attach a peer
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
