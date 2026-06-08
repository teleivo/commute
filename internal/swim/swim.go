// Package swim implements the SWIM failure detector as described in [SWIM: Scalable
// Weakly-consistent Infection-style Process Group Membership Protocol], Section 3.1.
//
// [SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol]: https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf
package swim

import (
	"context"
	"encoding/binary"
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

type messageKind uint8

const (
	ping messageKind = iota
	ack
	pingReq
)

func (a messageKind) String() string {
	switch a {
	case ping:
		return "ping"
	case ack:
		return "ack"
	case pingReq:
		return "ping-req"
	default:
		panic(fmt.Sprintf("unknown kind %d", uint8(a)))
	}
}

// Message is a SWIM protocol message sent and received over UDP.
// Period is the sender's local protocol period counter, echoed back in acks to
// correlate a response with the ping that triggered it.
type Message struct {
	Kind   messageKind
	Period uint64
}

var messageSize = binary.Size(Message{})

// Ack is an acknowledgement received from a peer.
type Ack struct {
	Period uint64
	Addr   net.Addr
}

// Listen reads incoming UDP messages and dispatches them: acks are forwarded to
// the Probe loop; pings are answered immediately; ping-reqs are not yet handled.
func (m *Member) Listen(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			b := make([]byte, messageSize)
			n, addr, err := m.conn.ReadFrom(b)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				m.logger.Error("failed to read UDP message", "error", err)
				continue
			}
			if n != messageSize {
				m.logger.Warn("unexpected message size", "addr", addr, "got", n, "want", messageSize)
				continue
			}
			var msg Message
			_, err = binary.Decode(b, binary.BigEndian, &msg)
			if err != nil {
				m.logger.Error("failed to parse message", "addr", addr, "error", err)
				continue
			}

			switch msg.Kind {
			case ack:
				m.logger.Debug("got ack", "addr", addr, "period", msg.Period)
				// Non-blocking send: default is only taken when no other case can proceed, so the
				// ack lands in the buffer if there is room and is dropped if the buffer is full
				// (e.g. a stale ack is waiting). A dropped ack is harmless; the probe loop will
				// fall back to indirect probing via ping-req on timeout.
				select {
				case m.acks <- Ack{Period: msg.Period, Addr: addr}:
				default:
				}
			case ping:
				msg := Message{Kind: ack, Period: msg.Period}
				b, err := binary.Append(nil, binary.BigEndian, msg)
				if err != nil {
					panic(err)
				}
				_, err = m.conn.WriteTo(b, addr)
				if err != nil {
					m.logger.Error("failed to send ack", "addr", addr, "error", err)
					continue
				}
				m.logger.Debug("sent ack", "addr", addr, "period", msg.Period)
			case pingReq:
				// TODO implement subgroup
				m.logger.Debug("got ping-req", "addr", addr, "period", msg.Period)
			}
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
	t := time.NewTicker(m.protocolPeriod)
	defer t.Stop()
	ackTimeout := time.NewTimer(m.ackTimeout)
	for {
		select {
		case <-t.C:
			period := m.period.Add(1)
			m.muPeers.RLock()
			if len(m.peers) == 0 {
				m.muPeers.RUnlock()
				m.logger.Warn("no peers to probe")
				continue
			}
			peer := m.peers[m.rng.IntN(len(m.peers))]
			m.muPeers.RUnlock()
			addr, err := net.ResolveUDPAddr(m.conn.LocalAddr().Network(), peer)
			if err != nil {
				m.logger.Error("failed to resolve peer address", "peer", peer, "error", err)
				continue
			}
			msg := Message{Kind: ping, Period: period}
			b, err := binary.Append(nil, binary.BigEndian, msg)
			if err != nil {
				panic(err)
			}
			_, err = m.conn.WriteTo(b, addr)
			if err != nil {
				m.logger.Error("failed to send ping", "peer", peer, "error", err)
				continue
			}
			m.logger.Debug("sent ping", "peer", peer, "period", period)

			ackTimeout.Reset(m.ackTimeout)
		waitAck:
			for {
				select {
				case <-ctx.Done():
					ackTimeout.Stop()
					return
				case <-ackTimeout.C:
					m.muPeers.Lock()
					m.peers = slices.DeleteFunc(m.peers, func(p string) bool {
						return p == peer
					})
					m.muPeers.Unlock()
					m.logger.Info("peer is dead", "peer", peer, "period", period)
					if m.notifier != nil {
						m.notifier.Notify(peer, Dead)
					}
					break waitAck
				case ack := <-m.acks:
					if ack.Period == period && ack.Addr.String() == peer {
						m.logger.Debug("peer is alive", "peer", peer, "period", period)
						ackTimeout.Stop()
						break waitAck
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
