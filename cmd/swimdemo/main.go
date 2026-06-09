package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/teleivo/commute/internal/swim"
)

// errFlagParse is a sentinel error indicating flag parsing failed.
// The flag package already printed the error, so main should not print again.
var errFlagParse = errors.New("flag parse error")

func main() {
	code, err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if err != nil && err != errFlagParse {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	os.Exit(code)
}

func run(args []string, _ io.Reader, _ io.Writer, wErr io.Writer) (int, error) {
	flags := flag.NewFlagSet("swimdemo", flag.ContinueOnError)
	flags.SetOutput(wErr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(wErr, "usage: swimdemo [flags]")
		_, _ = fmt.Fprintln(wErr, "flags:")
		flags.PrintDefaults()
	}
	nodes := flags.Uint("nodes", 3, "number of member nodes to start")
	protocolPeriod := flags.Duration("period", 1*time.Second, "SWIM protocol period T'")
	ackTimeout := flags.Duration("ack-timeout", 200*time.Millisecond, "how long to wait for a direct ack before indirect probing")
	subgroupSize := flags.Int("subgroup", 2, "indirect probing subgroup size k")
	debug := flags.Bool("debug", false, "enable debug logging")
	err := flags.Parse(args)
	if err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, errFlagParse
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	conns := make(map[string]*net.UDPConn, *nodes)
	for i := range *nodes {
		nodeID := fmt.Sprintf("node-%d", i)
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return 1, err
		}
		defer func() {
			_ = conn.Close()
		}()
		conns[nodeID] = conn
	}
	addrToID := make(map[string]string, *nodes)
	for id, conn := range conns {
		addrToID[conn.LocalAddr().String()] = id
	}
	peers := make(map[string][]string, *nodes)
	for i := range *nodes {
		nodeID := fmt.Sprintf("node-%d", i)
		for n, conn := range conns {
			if n != nodeID {
				peers[nodeID] = append(peers[nodeID], conn.LocalAddr().String())
			}
		}
	}

	events := make(chan event, int(*nodes))
	var wg sync.WaitGroup
	members := make(map[string]*swim.Member, *nodes)
	fns := make(map[string]context.CancelFunc, *nodes)
	for i := range *nodes {
		nodeID := fmt.Sprintf("node-%d", i)
		member, err := swim.New(swim.Config{
			NodeID:         nodeID,
			Conn:           conns[nodeID],
			Peers:          strings.Join(peers[nodeID], ","),
			ProtocolPeriod: *protocolPeriod,
			AckTimeout:     *ackTimeout,
			SubgroupSize:   *subgroupSize,
			Notifier: &notifier{nodeID: nodeID, events: events},
			Logger:   nodeLogger(nodeID, *debug, wErr),
		})
		if err != nil {
			return 1, err
		}
		members[nodeID] = member
		wg.Go(func() {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			fns[nodeID] = cancel
			_ = member.Start(ctx)
		})
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigCh {
			var nodeID string
			switch sig {
			case syscall.SIGUSR1:
				nodeID = "node-1"
			case syscall.SIGUSR2:
				nodeID = "node-2"
			}
			fns[nodeID]()
			events <- event{nodeID: nodeID, peer: nodeID, kind: memberDead}
		}
	}()

	nodeIDs := make([]string, 0, len(members))
	for id := range members {
		nodeIDs = append(nodeIDs, id)
	}
	wg.Go(func() {
		renderLoop(ctx, os.Stdout, nodeIDs, peers, addrToID, events, assetPath("scientist.png"), assetPath("network-side.png"))
	})

	wg.Wait()
	return 0, nil
}

// event carries a membership change detected by one node about a peer.
type event struct {
	nodeID string
	peer   string
	relay  string // set for indirectProbe events: the node asked to relay the ping-req
	kind   eventKind
}

type eventKind uint8

const (
	memberDead eventKind = iota
	memberIndirectProbe
)

// notifier implements swim.Notifier and forwards changes as events onto a channel.
type notifier struct {
	nodeID string
	events chan<- event
}

func (n *notifier) Notify(peer string, kind swim.EventKind) {
	if kind == swim.Dead {
		n.events <- event{nodeID: n.nodeID, peer: peer, kind: memberDead}
	}
}

func (n *notifier) NotifyIndirectProbe(peer, relay string) {
	n.events <- event{nodeID: n.nodeID, peer: peer, relay: relay, kind: memberIndirectProbe}
}

func renderLoop(ctx context.Context, w io.Writer, nodeIDs []string, peers map[string][]string, addrToID map[string]string, events <-chan event, scientistPNG, networkSidePNG string) {
	// dead[nodeID][peerID] = true when nodeID considers peerID dead
	dead := make(map[string]map[string]bool)
	// waiting[nodeID][peerID] = true when nodeID sent ping-reqs and is waiting for an indirect ack
	waiting := make(map[string]map[string]bool)
	// relaying[relayID][peerID] = true when relayID was asked to indirect-probe peerID
	relaying := make(map[string]map[string]bool)
	for _, id := range nodeIDs {
		dead[id] = make(map[string]bool)
		waiting[id] = make(map[string]bool)
		relaying[id] = make(map[string]bool)
	}

	render := func() {
		dot := buildDOT(nodeIDs, peers, addrToID, dead, waiting, relaying, scientistPNG, networkSidePNG)
		png, err := renderDOT(dot, 150)
		if err != nil {
			return
		}
		displayKitty(w, png)
	}

	render()

	debounce := time.NewTimer(0)
	<-debounce.C
	dirty := false

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			peerID := addrToID[e.peer]
			if peerID == "" {
				peerID = e.peer
			}
			relayID := addrToID[e.relay]
			if relayID == "" {
				relayID = e.relay
			}
			switch e.kind {
			case memberIndirectProbe:
				waiting[e.nodeID][peerID] = true
				if relayID != "" {
					relaying[relayID][peerID] = true
				}
			case memberDead:
				delete(waiting[e.nodeID], peerID)
				for _, id := range nodeIDs {
					delete(relaying[id], peerID)
				}
				dead[e.nodeID][peerID] = true
			}
			if !dirty {
				debounce.Reset(200 * time.Millisecond)
				dirty = true
			}
		case <-debounce.C:
			dirty = false
			render()
		}
	}
}

func buildDOT(nodeIDs []string, peers map[string][]string, addrToID map[string]string, dead map[string]map[string]bool, waiting map[string]map[string]bool, relaying map[string]map[string]bool, scientistPNG, networkSidePNG string) string {
	var b strings.Builder
	b.WriteString("digraph swim {\n")
	b.WriteString("  node [shape=circle]\n")
	for _, id := range nodeIDs {
		switch {
		case dead[id][id]:
			fmt.Fprintf(&b, "  %q [color=red fontcolor=red]\n", id)
		case len(relaying[id]) > 0:
			fmt.Fprintf(&b, "  %q [image=%q label=%q shape=none fixedsize=true imagescale=true width=1.2 height=1.2]\n", id, networkSidePNG, id)
		case len(waiting[id]) > 0:
			fmt.Fprintf(&b, "  %q [image=%q label=%q shape=none fixedsize=true imagescale=true width=1.2 height=1.2]\n", id, scientistPNG, id)
		default:
			fmt.Fprintf(&b, "  %q\n", id)
		}
	}
	for _, id := range nodeIDs {
		for _, peerAddr := range peers[id] {
			peerID := addrToID[peerAddr]
			var attrs string
			switch {
			case dead[id][peerID]:
				attrs = "color=red"
			case relaying[id][peerID]:
				attrs = "color=orange style=dashed"
			case waiting[id][peerID]:
				attrs = "color=gray style=dashed"
			default:
				attrs = "color=black"
			}
			fmt.Fprintf(&b, "  %q -> %q [%s]\n", id, peerID, attrs)
		}
	}
	b.WriteString("}\n")
	return b.String()
}

func nodeLogger(nodeID string, debug bool, w io.Writer) *slog.Logger {
	if !debug {
		return slog.New(slog.DiscardHandler)
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})).With("node_id", nodeID)
}

func renderDOT(dot string, dpi int) ([]byte, error) {
	cmd := exec.Command("dot", fmt.Sprintf("-Gdpi=%d", dpi), "-Gsize=18,18!", "-Tpng")
	cmd.Stdin = bytes.NewBufferString(dot)
	return cmd.Output()
}

func displayKitty(w io.Writer, png []byte) {
	b64 := base64.StdEncoding.EncodeToString(png)

	_, _ = fmt.Fprint(w, "\033[2J\033[H")

	const chunkSize = 4096
	for i := 0; i < len(b64); i += chunkSize {
		end := min(i+chunkSize, len(b64))
		chunk := b64[i:end]

		m := 1
		if end >= len(b64) {
			m = 0
		}

		if i == 0 {
			_, _ = fmt.Fprintf(w, "\033_Ga=T,f=100,m=%d;%s\033\\", m, chunk)
		} else {
			_, _ = fmt.Fprintf(w, "\033_Gm=%d;%s\033\\", m, chunk)
		}
	}
	_, _ = fmt.Fprintln(w)
}

// assetPath returns the absolute path to a file in the assets directory next to this source file.
// Gopher images in assets/ are from egonelbre/gophers (CC0): https://github.com/egonelbre/gophers
func assetPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "assets", name)
}
