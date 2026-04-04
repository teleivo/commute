package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/teleivo/commute/internal/crdt"
)

// Server is a CRDT key-value store node that serves an HTTP API and gossips state to peers.
type Server struct {
	logger         *slog.Logger
	server         *http.Server
	store          *Store
	peers          []string
	gossipInterval time.Duration
	client         *http.Client
	rng            *rand.Rand
}

// Config holds the configuration for creating a Server.
type Config struct {
	NodeID         string
	Addr           string        // listen address (e.g. ":8080", "0.0.0.0:8080")
	Peers          string        // comma-separated list of peer addresses (e.g. host1:7946,host2:7946)
	GossipInterval time.Duration // how often to push state to a random peer
	Client         *http.Client  // HTTP client for gossip
	Rng            *rand.Rand    // random source for peer selection
	Debug          bool          // enable debug logging
	Stderr         io.Writer     // output for error logging
}

// New creates a Server with the given configuration.
func New(cfg Config) (*Server, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	addr := cfg.Addr
	if addr == "" {
		addr = ":0"
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("invalid addr %q: %s", addr, err)
	}
	if cfg.Peers == "" {
		return nil, errors.New("at least one peer is required")
	}
	peers := make(map[string]struct{})
	for _, p := range strings.Split(cfg.Peers, ",") {
		p = strings.TrimSpace(p)
		host, port, err := net.SplitHostPort(p)
		if err != nil {
			return nil, fmt.Errorf("invalid peer %q: %s", p, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("invalid peer %q: host and port are required", p)
		}
		peers["http://"+p] = struct{}{}
	}
	if cfg.GossipInterval <= 0 {
		return nil, errors.New("gossip interval must be greater than zero")
	}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(cfg.Stderr, &slog.HandlerOptions{Level: level}))
	handler := http.NewServeMux()
	server := http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 3 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{}
	}
	rng := cfg.Rng
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	srv := &Server{
		logger:         logger,
		server:         &server,
		store:          NewStore(crdt.NodeID(cfg.NodeID)),
		peers:          slices.Sorted(maps.Keys(peers)),
		gossipInterval: cfg.GossipInterval,
		client:         client,
		rng:            rng,
	}
	handler.HandleFunc("GET /types/counters/keys/{key}", srv.getCounters)
	handler.HandleFunc("POST /types/counters/keys/{key}", srv.postCounters)
	handler.HandleFunc("POST /internal/gossip", srv.postGossip)
	return srv, nil
}

// Start begins serving HTTP and gossiping state to peers. It blocks until the context is cancelled.
func (srv *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", srv.server.Addr)
	if err != nil {
		return err
	}
	srv.logger.Info("listening", "addr", ln.Addr())

	go func() {
		<-ctx.Done()
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := srv.server.Shutdown(ctxTimeout); err != nil && !errors.Is(err, context.Canceled) {
			srv.logger.Error("failed to shutdown", "err", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		srv.StartGossip(ctx)
	})

	if err := srv.server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	wg.Wait()
	return nil
}

// StartGossip runs the gossip loop, periodically pushing full state to a random peer. It blocks
// until the context is cancelled.
func (srv *Server) StartGossip(ctx context.Context) {
	t := time.NewTicker(srv.gossipInterval)
	defer t.Stop()
	timeout := srv.gossipInterval / 2
	for {
		select {
		case <-t.C:
			peer := srv.peers[srv.rng.IntN(len(srv.peers))]
			srv.store.muCounters.RLock()
			b, err := json.Marshal(srv.store.counters)
			srv.store.muCounters.RUnlock()
			if err != nil {
				panic(err)
			}

			reqCtx, cancel := context.WithTimeout(ctx, timeout)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, peer+"/internal/gossip", bytes.NewReader(b))
			if err != nil {
				cancel()
				panic(err)
			}
			req.Header.Add("Content-Type", "application/json")
			resp, err := srv.client.Do(req)
			cancel()
			if err != nil {
				srv.logger.Warn("failed to gossip full state", "peer", peer, "error", err)
				continue
			}
			srv.logger.Debug("gossiped full state", "peer", peer)
			_ = resp.Body.Close()
		case <-ctx.Done():
			return
		}
	}
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	srv.server.Handler.ServeHTTP(w, r)
}

type counterRequestBody struct {
	Increment uint64 `json:"increment"`
	Decrement uint64 `json:"decrement"`
}

type counterResponseBody struct {
	Value int64 `json:"value"`
}

func (srv *Server) getCounters(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	value, ok := srv.store.GetCounter(key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	resp := counterResponseBody{
		Value: value,
	}
	e := json.NewEncoder(w)
	err := e.Encode(resp)
	if err != nil {
		srv.logger.Error("failed to encode counter value", "err", err)
	}
}

func (srv *Server) postCounters(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Body == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var body counterRequestBody
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if (body.Increment > 0 && body.Decrement > 0) || (body.Increment == 0 && body.Decrement == 0) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Increment > 0 {
		srv.store.IncrementCounter(key, body.Increment)
	} else {
		srv.store.DecrementCounter(key, body.Decrement)
	}

	w.WriteHeader(http.StatusOK)
}

func (srv *Server) postGossip(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var body Message
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	srv.store.Merge(body)

	w.WriteHeader(http.StatusOK)
}
