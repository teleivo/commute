// Package swim implements the SWIM failure detector as described in [SWIM: Scalable
// Weakly-consistent Infection-style Process Group Membership Protocol], Section 4.
//
// [SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol]: https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf
package swim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Member is a node participating in the SWIM failure detection protocol.
type Member struct {
	nodeID   string
	conn     net.PacketConn
	listener net.Listener
	server   *http.Server

	seeds      []string
	httpClient *http.Client
	resolve    func(addr string) (net.Addr, error)

	// muPeers guards peers and deadPeers
	peers     []string
	deadPeers map[string]struct{}
	muPeers   sync.RWMutex

	protocolPeriod      time.Duration
	ackTimeout          time.Duration
	subgroupSize        int
	disseminationFactor int
	period              atomic.Uint64

	rng        *rand.Rand
	muRng      sync.Mutex
	acks       chan Ack
	eventQueue EventQueue
	notifier   Notifier
	logger     *slog.Logger
}

// Config holds the configuration for creating a Member.
type Config struct {
	NodeID   string
	Conn     net.PacketConn // UDP connection to receive and send packets on
	Listener net.Listener   // TCP listener for the HTTP join endpoint

	Seeds      string                              // comma-separated list of seed HTTP addresses for the bootstrap loop (e.g. host1:7947,host2:7947)
	HTTPClient *http.Client                        // HTTP client for bootstrap join calls; if nil, defaults to http.DefaultClient
	Resolve    func(addr string) (net.Addr, error) // if nil, defaults to net.ResolveUDPAddr

	ProtocolPeriod      time.Duration // T' in the paper: duration of one failure detection round
	AckTimeout          time.Duration // how long to wait for a direct ack before declaring a peer dead
	SubgroupSize        int           // k in the paper: number of members used for indirect probing
	DisseminationFactor int           // multiplier for membership event dissemination count; events are piggybacked DisseminationFactor·log(N) times (SWIM paper Section 4.1)

	Rng      *rand.Rand   // random source for peer selection
	Notifier Notifier     // if nil, membership changes are not reported
	Logger   *slog.Logger // if nil, logging is disabled
}

// New creates a Member from the given Config.
func New(cfg Config) (*Member, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	if cfg.Conn == nil {
		return nil, errors.New("conn is required")
	}
	resolve := cfg.Resolve
	if resolve == nil {
		resolve = func(addr string) (net.Addr, error) {
			return net.ResolveUDPAddr("udp", addr)
		}
	}
	seeds := make(map[string]struct{})
	for s := range strings.SplitSeq(cfg.Seeds, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return nil, fmt.Errorf("invalid seed %q: %s", s, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("invalid seed %q: host and port are required", s)
		}
		seeds[s] = struct{}{}
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
	disseminationFactor := cfg.DisseminationFactor
	if disseminationFactor <= 0 {
		disseminationFactor = 3
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
	handler := http.NewServeMux()
	server := http.Server{
		Handler:     handler,
		ReadTimeout: 3 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	m := &Member{
		nodeID:              cfg.NodeID,
		conn:                cfg.Conn,
		listener:            cfg.Listener,
		server:              &server,
		seeds:               slices.Collect(maps.Keys(seeds)),
		httpClient:          client,
		resolve:             resolve,
		peers:               make([]string, 0),
		deadPeers:           make(map[string]struct{}),
		protocolPeriod:      cfg.ProtocolPeriod,
		ackTimeout:          cfg.AckTimeout,
		subgroupSize:        cfg.SubgroupSize,
		disseminationFactor: disseminationFactor,
		rng:                 rng,
		acks:                make(chan Ack, 1), // shared across rounds; a stale ack can evict a live one via the non-blocking send in Listen
		eventQueue:          EventQueue{},
		notifier:            cfg.Notifier,
		logger:              logger,
	}
	handler.HandleFunc("POST /internal/swim/join", m.JoinHandler)
	return m, nil
}

// Start runs the Listen and Probe loops until ctx is cancelled.
func (m *Member) Start(ctx context.Context) error {
	m.logger.Info("listening", "addr", m.conn.LocalAddr(), "tcpAddr", m.listener.Addr())

	go func() {
		<-ctx.Done()
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := m.server.Shutdown(ctxTimeout); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Error("failed to shutdown", "error", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		m.Bootstrap(ctx)
	})
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

	if err := m.server.Serve(m.listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
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

func (m *Member) Addr() string {
	addr := m.conn.LocalAddr().(*net.UDPAddr)
	return m.nodeID + ":" + strconv.Itoa(addr.Port)
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

		for _, e := range msg.Events {
			if e.Kind == Dead {
				m.logger.Info("peer is dead", "peer", e.Node, "source", addr)
				m.deletePeer(EventItem{Event: Event{Kind: Dead, Node: e.Node}})
			}
		}

		m.logger.Debug("got message", "addr", addr, "src", msg.Src, "kind", msg.Kind, "period", msg.Period, "target", msg.Target)
		switch msg.Kind {
		case ack:
			ackAddr := msg.Src
			if msg.Target != "" {
				// relay ack carries the original probe target in Target so we can route it
				// to the right relay waiter and deliver an Ack as if it came from the target
				ackAddr = msg.Target
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
			reply := NewMessage(ack, m.Addr(), msg.Period, "")
			_ = m.sendToAddr(addr.String(), addr, reply)
		case pingReq:
			if msg.Target == "" {
				m.logger.Warn("message is missing required target for indirect ping", "addr", addr)
				continue
			}

			key := relayKey{target: msg.Target, period: msg.Period}
			ackCh := make(chan Ack, 1)
			mu.Lock()
			relayAcks[key] = ackCh
			mu.Unlock()

			go func(acks <-chan Ack, done func()) {
				defer done()

				target := msg.Target
				if err := m.send(target, NewMessage(ping, m.Addr(), msg.Period, "")); err != nil {
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
							_ = m.sendToAddr(addr.String(), addr, NewMessage(ack, m.Addr(), msg.Period, target))
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

// Notifier is called by a Member when a peer's membership status changes. Notify is called in a
// goroutine so implementations may block without affecting the probe loop. peer is the SWIM UDP
// address as given in [Config].Peers (e.g. "node-1:7946"), not any application-layer address.
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
			m.logger.Warn("no peers to send ping to")
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
		if err := m.send(peer, NewMessage(ping, m.Addr(), period, "")); err != nil {
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
				indirects, ok := m.kRandomPeers(peer)
				if !ok {
					m.logger.Warn("no peers to send ping-req to")
					continue
				}

				for _, indirect := range indirects {
					_ = m.send(indirect, NewMessage(pingReq, m.Addr(), period, peer))
				}
			case <-periodTimer.C:
				// period ended without getting an ack so peer is declared dead
				ackTimeout.Stop()
				m.logger.Info("peer is dead", "peer", peer, "period", period)
				m.deletePeer(EventItem{Event: Event{Kind: Dead, Node: peer}})
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

func (m *Member) kRandomPeers(target string) ([]string, bool) {
	m.muPeers.RLock()
	candidates := make([]string, 0, len(m.peers))
	for _, p := range m.peers {
		if p != target {
			candidates = append(candidates, p)
		}
	}
	m.muPeers.RUnlock()

	if len(candidates) == 0 {
		return nil, false
	}

	if len(candidates) <= m.subgroupSize {
		return candidates, true
	}

	subgroup := make(map[string]struct{}, m.subgroupSize)
	m.muRng.Lock()
	for range 3 * len(candidates) {
		node := candidates[m.rng.IntN(len(candidates))]
		if _, ok := subgroup[node]; ok {
			continue
		}
		subgroup[node] = struct{}{}
		if len(subgroup) == m.subgroupSize {
			break
		}
	}
	m.muRng.Unlock()
	return slices.Collect(maps.Keys(subgroup)), true
}

func (m *Member) deletePeer(item EventItem) {
	var newEvent bool
	m.muPeers.Lock()
	m.peers = slices.DeleteFunc(m.peers, func(p string) bool {
		if p == item.Event.Node {
			newEvent = true
			return true
		}
		return false
	})
	m.deadPeers[item.Event.Node] = struct{}{}
	m.muPeers.Unlock()

	// Peer was already removed (e.g. dead event piggybacked while Probe was also declaring it
	// dead). Skip enqueue and notification to avoid duplicate dissemination.
	if !newEvent {
		return
	}

	m.eventQueue.Push(item)
	if m.notifier != nil {
		go m.notifier.Notify(item.Event.Node, Dead)
	}
}

func (m *Member) randomPeer() string {
	m.muRng.Lock()
	i := m.rng.IntN(len(m.peers))
	m.muRng.Unlock()
	return m.peers[i]
}

// send delivers msg to peer by resolving its address and writing the message.
func (m *Member) send(peer string, msg Message) error {
	addr, err := m.resolve(peer)
	if err != nil {
		m.logger.Error("failed to resolve address for peer", "peer", peer)
		return fmt.Errorf("failed to resolve address for peer %q", peer)
	}
	return m.sendToAddr(peer, addr, msg)
}

// sendToAddr delivers msg to peer and piggybacks pending membership events onto it.
func (m *Member) sendToAddr(peer string, addr net.Addr, msg Message) error {
	items := m.eventQueue.Pop(maxPiggybackEvents)
	for _, item := range items {
		msg.Events = append(msg.Events, item.Event)
	}

	err := m.writeMessage(peer, addr, msg)
	if err != nil {
		m.eventQueue.Push(items...)
		return err
	}

	m.muPeers.RLock()
	maxDisseminations := int(math.Ceil(float64(m.disseminationFactor) * math.Log2(float64(len(m.peers)+1))))
	m.muPeers.RUnlock()
	end := 0
	for i := range len(items) {
		items[i].SendCount++
		if items[i].SendCount < maxDisseminations {
			items[end] = items[i]
			end++
		}
	}
	m.eventQueue.Push(items[:end]...)

	return nil
}

func (m *Member) writeMessage(peer string, addr net.Addr, msg Message) error {
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

// Bootstrap runs the seed join loop until ctx is cancelled. It contacts each configured seed over
// HTTP using a push/pull exchange: the node sends its current peer list in the request body so the
// seed can learn new members (push), and the seed returns its own member list in the response so
// the caller can discover indirect peers (pull). Seeds that are unreachable are retried with
// exponential backoff (initial 5 s, doubling up to 5 min, with jitter); seeds that never respond
// are never added as peers and never enter the failure detector.
//
// HTTP is used instead of UDP for the initial join as a pragmatic choice. A pure UDP join
// subprotocol would require retransmission logic and message framing for large member lists.
// QUIC would address that but adds an external dependency. HTTP gives reliable delivery with no
// extra dependency since the server is already in place, and fits the time constraints of this
// learning project.
func (m *Member) Bootstrap(ctx context.Context) {
	jitter := func(interval time.Duration) time.Duration {
		m.muRng.Lock()
		j := m.rng.Int64N(int64(min(interval/6, 5*time.Second)))
		m.muRng.Unlock()
		return time.Duration(j)
	}
	interval := 5 * time.Second
	joinTimeout := 500 * time.Millisecond
	wait := time.NewTimer(0)
	defer wait.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		}

		m.muPeers.Lock()
		peers := slices.Clone(m.peers)
		m.muPeers.Unlock()
		peers = append(peers, m.Addr())
		body, err := json.Marshal(joinBody{
			Peers: peers,
		})
		if err != nil {
			panic(err)
		}

		var discovered []string
		for _, seed := range m.seeds {
			reqCtx, cancel := context.WithTimeout(ctx, joinTimeout)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+seed+"/internal/swim/join", bytes.NewReader(body))
			if err != nil {
				cancel()
				panic(err)
			}
			req.Header.Add("Content-Type", "application/json")
			resp, err := m.httpClient.Do(req)
			cancel()
			if err != nil {
				m.logger.Warn("failed to join seed", "seed", seed, "error", err)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				m.logger.Warn("join rejected by seed", "seed", seed, "status", resp.StatusCode)
				continue
			}

			var joined joinBody
			err = json.NewDecoder(resp.Body).Decode(&joined)
			_ = resp.Body.Close()
			if err != nil {
				m.logger.Warn("failed to decode join response", "seed", seed, "error", err)
				continue
			}
			m.logger.Debug("joined seed", "seed", seed)
			discovered = append(discovered, joined.Peers...)
		}

		self := m.Addr()
		m.muPeers.Lock()
		for _, p := range discovered {
			if p == self {
				continue
			}
			if _, ok := m.deadPeers[p]; ok {
				continue
			}
			if !slices.Contains(m.peers, p) {
				m.peers = append(m.peers, p)
			}
		}
		m.muPeers.Unlock()

		wait.Reset(interval + jitter(interval))
		if interval < 5*time.Minute {
			interval = min(interval*2, 5*time.Minute)
		}
	}
}

type joinBody struct {
	Peers []string `json:"peers"`
}

// JoinHandler returns an HTTP handler that accepts POST /internal/swim/join requests. The request
// body must contain the caller's SWIM UDP address (host:port). The handler registers the caller as
// a peer and responds with the node's current SWIM member list as a JSON array of addresses.
func (m *Member) JoinHandler(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req joinBody
	err = json.Unmarshal(b, &req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	m.muPeers.Lock()
	for _, p := range req.Peers {
		if p == m.Addr() {
			continue
		}
		if _, ok := m.deadPeers[p]; ok {
			continue
		}
		if !slices.Contains(m.peers, p) {
			m.peers = append(m.peers, p)
		}
	}
	result := slices.Clone(m.peers)
	result = append(result, m.Addr())
	m.muPeers.Unlock()

	e := json.NewEncoder(w)
	err = e.Encode(joinBody{
		Peers: result,
	})
	if err != nil {
		m.logger.Error("failed to encode peers", "error", err)
	}
}
