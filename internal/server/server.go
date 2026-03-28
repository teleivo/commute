package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/teleivo/commute/internal/crdt"
)

type Server struct {
	logger *slog.Logger
	server *http.Server
	store  *Store
}

type Config struct {
	NodeID string
	Port   string    // HTTP server port (use "0" for a random available port)
	Debug  bool      // enable debug logging
	Stderr io.Writer // output for error logging
}

func New(cfg Config) (*Server, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("node ID is required")
	}
	addr, err := netip.ParseAddrPort("127.0.0.1:" + cfg.Port)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q, must be in range 1-65535", cfg.Port)
	}

	handler := http.NewServeMux()
	server := http.Server{
		Addr:        addr.String(),
		Handler:     handler,
		ReadTimeout: 3 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(cfg.Stderr, &slog.HandlerOptions{Level: level}))
	srv := &Server{
		logger: logger,
		server: &server,
		store:  NewStore(crdt.NodeID(cfg.NodeID)),
	}
	handler.HandleFunc("GET /types/counters/keys/{key}", srv.getCounters)
	handler.HandleFunc("POST /types/counters/keys/{key}", srv.postCounters)
	return srv, nil
}

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

	if err := srv.server.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	srv.server.Handler.ServeHTTP(w, r)
}

type counterRequestBody struct {
	Increment uint64 `json:"increment"`
}

type counterResponseBody struct {
	Value uint64 `json:"value"`
}

type Store struct {
	nodeID     crdt.NodeID
	muCounters sync.RWMutex
	counters   map[string]*crdt.GCounter
}

func NewStore(nodeID crdt.NodeID) *Store {
	return &Store{
		nodeID:   nodeID,
		counters: make(map[string]*crdt.GCounter),
	}
}

func (srv *Server) getCounters(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	srv.store.muCounters.RLock()
	counter, ok := srv.store.counters[key]
	if !ok {
		srv.store.muCounters.RUnlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	value := counter.Value()
	srv.store.muCounters.RUnlock()

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
	defer func() {
		_ = r.Body.Close()
	}()
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

	srv.store.muCounters.Lock()
	counter, ok := srv.store.counters[key]
	if !ok {
		counter = crdt.NewGCounter(srv.store.nodeID)
		srv.store.counters[key] = counter
	}
	counter.Increment(body.Increment)
	srv.store.muCounters.Unlock()

	w.WriteHeader(http.StatusOK)
}
