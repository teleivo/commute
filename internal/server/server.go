// Package server implements a CRDT key-value store node with an HTTP API and gossip-based
// state replication.
package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/teleivo/commute/internal/crdt"
	"github.com/teleivo/commute/internal/swim"
)

// Server is a CRDT key-value store node that serves an HTTP API and gossips state to peers.
type Server struct {
	logger         *slog.Logger
	nodeID         string
	listener       net.Listener
	advertiseAddr  string
	server         *http.Server
	store          *Store
	peers          []string
	peersMu        sync.RWMutex
	gossipInterval time.Duration
	client         *http.Client
	rng            *rand.Rand
}

// Config holds the configuration for creating a Server.
type Config struct {
	NodeID         string
	Listener       net.Listener  // listener to accept connections on
	AdvertiseAddr  string        // address advertised to peers (ip:port); must match how peers address this node
	GossipInterval time.Duration // how often to push state to a random peer
	Client         *http.Client  // HTTP client for gossip
	Rng            *rand.Rand    // random source for peer selection
	Clock          crdt.Clock    // clock for LWW timestamps
	Logger         *slog.Logger  // if nil, logging is disabled
}

// New creates a Server with the given configuration.
func New(cfg Config) (*Server, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	if cfg.AdvertiseAddr == "" {
		return nil, errors.New("advertise address is required")
	}
	if host, port, err := net.SplitHostPort(cfg.AdvertiseAddr); err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("invalid advertise address %q: must be host:port", cfg.AdvertiseAddr)
	}
	if cfg.GossipInterval <= 0 {
		return nil, errors.New("gossip interval must be greater than zero")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	logger = logger.With(
		slog.String("component", "server"),
		slog.String("node_id", cfg.NodeID),
		slog.String("advertise_addr", cfg.AdvertiseAddr),
	)
	handler := http.NewServeMux()
	server := http.Server{
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
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	srv := &Server{
		logger:         logger,
		nodeID:         cfg.NodeID,
		listener:       cfg.Listener,
		advertiseAddr:  cfg.AdvertiseAddr,
		server:         &server,
		store:          NewStore(crdt.NodeID(cfg.NodeID), clock),
		gossipInterval: cfg.GossipInterval,
		client:         client,
		rng:            rng,
	}
	handler.HandleFunc("GET /counters/{key}", srv.getCounters)
	handler.HandleFunc("POST /counters/{key}", srv.postCounters)
	handler.HandleFunc("GET /registers/{key}", srv.getRegister)
	handler.HandleFunc("PUT /registers/{key}", srv.putRegister)
	handler.HandleFunc("GET /sets/{key}", srv.getSet)
	handler.HandleFunc("POST /sets/{key}", srv.postSet)
	handler.HandleFunc("POST /internal/gossip", srv.postGossip)
	handler.HandleFunc("POST /internal/ack", srv.postAck)

	httpRequestsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "commute_http_requests_total",
		Help:        "Total HTTP requests by route pattern, status code, and node.",
		ConstLabels: prometheus.Labels{"node": cfg.NodeID},
	}, []string{"path", "status"})
	httpRequestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "commute_http_request_duration_seconds",
		Help:        "HTTP request latency by route pattern, status code, and node.",
		ConstLabels: prometheus.Labels{"node": cfg.NodeID},
		Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"path", "status"})

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(
			collectors.WithGoCollectorRuntimeMetrics(
				collectors.GoRuntimeMetricsRule{Matcher: regexp.MustCompile(`^/sync/mutex/wait/total:seconds`)},
			),
		),
		newStoreCollector(srv.store, cfg.NodeID),
		httpRequestsTotal,
		httpRequestDuration,
	)
	handler.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	server.Handler = httpMetricsMiddleware(handler, httpRequestsTotal, httpRequestDuration)

	return srv, nil
}

// Start begins serving HTTP and gossiping state to peers. It blocks until the context is cancelled.
func (srv *Server) Start(ctx context.Context) error {
	srv.logger.Info("listening", "addr", srv.listener.Addr())

	go func() {
		<-ctx.Done()
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := srv.server.Shutdown(ctxTimeout); err != nil && !errors.Is(err, context.Canceled) {
			srv.logger.Error("failed to shutdown", "error", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		srv.StartGossip(ctx)
	})

	if err := srv.server.Serve(srv.listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	wg.Wait()
	return nil
}

// StartGossip runs the gossip loop, periodically pushing full state to a random peer. It blocks
// until the context is cancelled.
func (srv *Server) StartGossip(ctx context.Context) {
	logger := srv.logger.With("loop", "gossip")
	t := time.NewTicker(srv.gossipInterval)
	defer t.Stop()
	timeout := srv.gossipInterval / 2
	for {
		select {
		case <-t.C:
			srv.peersMu.RLock()
			if len(srv.peers) == 0 {
				srv.peersMu.RUnlock()
				continue
			}
			peer := srv.peers[srv.rng.IntN(len(srv.peers))]
			srv.peersMu.RUnlock()

			b, ok := srv.store.Delta(peer)
			if !ok {
				continue
			}

			reqCtx, cancel := context.WithTimeout(ctx, timeout)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+peer+"/internal/gossip", bytes.NewReader(b))
			if err != nil {
				cancel()
				panic(err)
			}
			req.Header.Add("Content-Type", "application/json")
			req.Header.Add("X-Node-Addr", srv.advertiseAddr)
			resp, err := srv.client.Do(req)
			cancel()
			if err != nil {
				logger.Warn("failed to gossip state", "peer", peer, "error", err)
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				logger.Warn("gossip rejected", "peer", peer, "status", resp.StatusCode)
				continue
			}
			logger.Debug("gossiped state", "peer", peer)
		case <-ctx.Done():
			return
		}
	}
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	srv.server.Handler.ServeHTTP(w, r)
}

type counterRequest struct {
	Increment uint64 `json:"increment"`
	Decrement uint64 `json:"decrement"`
}

type counterResponse struct {
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

	resp := counterResponse{
		Value: value,
	}
	e := json.NewEncoder(w)
	err := e.Encode(resp)
	if err != nil {
		srv.logger.Error("failed to encode counter value", "error", err)
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

	var body counterRequest
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

type registerBody struct {
	Value json.RawMessage `json:"value"`
}

func (srv *Server) getRegister(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	value, ok := srv.store.GetRegister(key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	resp := registerBody{
		Value: value,
	}
	e := json.NewEncoder(w)
	err := e.Encode(resp)
	if err != nil {
		srv.logger.Error("failed to encode register value", "error", err)
	}
}

func (srv *Server) putRegister(w http.ResponseWriter, r *http.Request) {
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

	var body registerBody
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Value == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	srv.store.SetRegister(key, body.Value)

	w.WriteHeader(http.StatusOK)
}

type setRequestBody struct {
	Add      []string          `json:"add"`
	Remove   []string          `json:"remove"`
	Contexts map[string]string `json:"contexts"`
}

type setResponseBody struct {
	Values   []string          `json:"values"`
	Contexts map[string]string `json:"contexts"`
}

func (srv *Server) getSet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	values, vvs, ok := srv.store.GetSet(key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if values == nil {
		values = []string{}
	}
	contexts, err := encodeContexts(vvs)
	if err != nil {
		srv.logger.Error("failed to encode set contexts", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	resp := setResponseBody{
		Values:   values,
		Contexts: contexts,
	}
	e := json.NewEncoder(w)
	err = e.Encode(resp)
	if err != nil {
		srv.logger.Error("failed to encode set value", "error", err)
	}
}

// encodeContexts marshals each version vector to JSON and base64-encodes it for transport in the
// HTTP response.
func encodeContexts(vvs map[string]crdt.VV) (map[string]string, error) {
	contexts := make(map[string]string, len(vvs))
	for k, vv := range vvs {
		raw, err := json.Marshal(vv)
		if err != nil {
			return nil, err
		}
		contexts[k] = base64.StdEncoding.EncodeToString(raw)
	}
	return contexts, nil
}

func (srv *Server) postSet(w http.ResponseWriter, r *http.Request) {
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

	var body setRequestBody
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if len(body.Add) == 0 && len(body.Remove) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	vvs := make(map[string]crdt.VV, len(body.Contexts))
	for k, v := range body.Contexts {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var vv crdt.VV
		err = json.Unmarshal(raw, &vv)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		vvs[k] = vv
	}

	// Removes run before adds so a request that targets the same element with both ends with the
	// element re-added under a fresh dot, matching Riak's set semantics.
	// vvs[v] returns the zero VV for elements without a client-supplied context, which is the
	// correct "no observation" input for DVVSet.Update.
	for _, v := range body.Remove {
		srv.store.RemoveSet(key, v, vvs[v])
	}
	for _, v := range body.Add {
		srv.store.AddSet(key, v, vvs[v])
	}

	values, serverVVs, ok := srv.store.GetSet(key)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if values == nil {
		values = []string{}
	}
	contexts, err := encodeContexts(serverVVs)
	if err != nil {
		srv.logger.Error("failed to encode set contexts", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	resp := setResponseBody{
		Values:   values,
		Contexts: contexts,
	}
	e := json.NewEncoder(w)
	err = e.Encode(resp)
	if err != nil {
		srv.logger.Error("failed to encode set value", "error", err)
	}
}

func (srv *Server) postGossip(w http.ResponseWriter, r *http.Request) {
	sender, err := srv.parseNodeAddr(r)
	if err != nil {
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

	var body Message
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	ackMsg := srv.store.Merge(body)
	go srv.sendAck(ackMsg, sender)

	w.WriteHeader(http.StatusOK)
}

func (srv *Server) sendAck(ackMsg AckMessage, sender string) {
	ack, err := json.Marshal(ackMsg)
	if err != nil {
		srv.logger.Error("failed to marshal ack message", "peer", sender, "error", err)
		return
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+sender+"/internal/ack", bytes.NewReader(ack))
	if err != nil {
		cancel()
		panic(err)
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("X-Node-Addr", srv.advertiseAddr)
	resp, err := srv.client.Do(req)
	cancel()
	if err != nil {
		srv.logger.Warn("failed to ack gossip state", "peer", sender, "error", err)
		return
	}
	srv.logger.Debug("acked gossiped state", "peer", sender)
	_ = resp.Body.Close()
}

func (srv *Server) postAck(w http.ResponseWriter, r *http.Request) {
	sender, err := srv.parseNodeAddr(r)
	if err != nil {
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

	var body AckMessage
	err = json.Unmarshal(b, &body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	srv.store.Ack(sender, body)

	w.WriteHeader(http.StatusOK)
}

func (srv *Server) parseNodeAddr(r *http.Request) (string, error) {
	nodeAddr := r.Header["X-Node-Addr"]
	if nodeAddr == nil {
		return "", errors.New("header X-Node-Addr missing")
	}
	if len(nodeAddr) > 1 {
		return "", errors.New("multiple X-Node-Addr headers")
	}
	peer := nodeAddr[0]
	if peer == "" {
		return "", errors.New("header X-Node-Addr empty")
	}
	srv.peersMu.RLock()
	defer srv.peersMu.RUnlock()
	if !slices.Contains(srv.peers, peer) {
		return "", fmt.Errorf("unknown peer in X-Node-Addr: %q", peer)
	}
	return peer, nil
}

// Notify implements [swim.Notifier].
func (srv *Server) Notify(peer swim.Peer, kind swim.EventKind) {
	httpAddr := peer.HTTPAddr()
	switch kind {
	case swim.Alive:
		srv.peersMu.Lock()
		if !slices.Contains(srv.peers, httpAddr) {
			srv.peers = append(srv.peers, httpAddr)
		}
		srv.peersMu.Unlock()
	case swim.Dead:
		host, _, _ := strings.Cut(peer.UDPAddr(), ":")
		srv.peersMu.Lock()
		srv.peers = slices.DeleteFunc(srv.peers, func(p string) bool {
			return strings.HasPrefix(p, host+":")
		})
		srv.peersMu.Unlock()
	}
}
