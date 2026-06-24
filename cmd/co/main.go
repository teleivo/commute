package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"syscall"
	"time"

	"github.com/teleivo/commute/internal/server"
	"github.com/teleivo/commute/internal/swim"
	"github.com/teleivo/commute/internal/version"
)

// errFlagParse is a sentinel error indicating flag parsing failed.
// The flag package already printed the error, so main should not print again.
var errFlagParse = errors.New("flag parse error")

func main() {
	code, err := run(os.Args, os.Stdout, os.Stderr)
	if err != nil && err != errFlagParse {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	os.Exit(code)
}

func run(args []string, w io.Writer, wErr io.Writer) (int, error) {
	if len(args) < 2 {
		usage(wErr)
		return 2, nil
	}

	if args[1] == "-h" || args[1] == "--help" || args[1] == "help" {
		usage(wErr)
		return 0, nil
	}

	switch args[1] {
	case "version":
		_, _ = fmt.Fprintln(w, version.Version())
		return 0, nil
	case "server":
		return runServer(args[2:], wErr)
	case "":
		return 2, errors.New("no command specified")
	default:
		return 2, fmt.Errorf("unknown command: %s", args[1])
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "co is a CRDT-based key-value store")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "usage: co <command> [args]")
	_, _ = fmt.Fprintln(w, "commands: server, version")
}

func profile(fn func() error, cpuProfile, memProfile, traceProfile string) error {
	if cpuProfile != "" {
		f, err := os.Create(cpuProfile)
		if err != nil {
			return fmt.Errorf("could not create CPU profile: %v", err)
		}
		defer func() { _ = f.Close() }()
		if err := pprof.StartCPUProfile(f); err != nil {
			return fmt.Errorf("could not start CPU profile: %v", err)
		}
		defer pprof.StopCPUProfile()
	}
	if traceProfile != "" {
		f, err := os.Create(traceProfile)
		if err != nil {
			return fmt.Errorf("could not create trace: %v", err)
		}
		defer func() { _ = f.Close() }()
		if err := trace.Start(f); err != nil {
			return fmt.Errorf("could not start trace: %v", err)
		}
		defer trace.Stop()
	}

	err := fn()
	if err != nil {
		return err
	}

	if memProfile != "" {
		f, err := os.Create(memProfile)
		if err != nil {
			return fmt.Errorf("could not create memory profile: %v", err)
		}
		defer func() { _ = f.Close() }()
		runtime.GC() // materialize all statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			return fmt.Errorf("could not write memory profile: %v", err)
		}
	}

	return nil
}

func runServer(args []string, wErr io.Writer) (int, error) {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(wErr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(wErr, "usage: co server [flags]")
		_, _ = fmt.Fprintln(wErr, "flags:")
		flags.PrintDefaults()
	}
	nodeID := flags.String("node-id", "", "unique node identifier (required)")
	addr := flags.String("addr", ":0", "HTTP listen address for the KV API and CRDT state gossip (e.g. :8080)")
	advertiseHost := flags.String("advertise-host", "", "hostname peers use to reach this node for all UDP and HTTP traffic")
	gossipInterval := flags.Duration("gossip-interval", 5*time.Second, "how often to push state to a random peer")

	swimAddr := flags.String("swim-addr", ":0", "UDP listen address for SWIM failure detection (e.g. :7946)")
	swimJoinAddr := flags.String("swim-join-addr", ":0", "TCP listen address for the SWIM HTTP join endpoint (e.g. :7947)")
	swimSeeds := flags.String("swim-seeds", "", "comma-separated list of seed HTTP addresses for bootstrap (e.g. host1:7947,host2:7947)")
	swimProtocolPeriod := flags.Duration("swim-protocol-period", 2*time.Second, "SWIM protocol period")
	swimAckTimeout := flags.Duration("swim-ack-timeout", 500*time.Millisecond, "direct ack wait duration before probing indirectly")
	swimSuspicionTimeout := flags.Duration("swim-suspicion-timeout", 4*time.Second, "how long a suspected peer has to refute before being declared dead")
	swimSubgroupSize := flags.Int("swim-subgroup-size", 3, "number of nodes used for indirect probing")
	swimDisseminationFactor := flags.Int("swim-dissemination-factor", 3, "multiplier for membership event dissemination count; events are piggybacked disseminationFactor·log(N) times")

	debug := flags.Bool("debug", false, "enable debug logging")
	cpuProfile := flags.String("cpu-profile", "", "write cpu profile to `file`")
	memProfile := flags.String("mem-profile", "", "write memory profile to `file`")
	traceProfile := flags.String("trace", "", "write execution trace to `file`")

	err := flags.Parse(args)
	if err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, errFlagParse
	}

	if *nodeID == "" {
		return 2, errors.New("node-id is required")
	}
	if _, _, err := net.SplitHostPort(*addr); err != nil {
		return 2, fmt.Errorf("invalid addr %q: %s", *addr, err)
	}
	if *advertiseHost == "" {
		return 2, errors.New("advertise-host is required")
	}
	if _, _, err := net.SplitHostPort(*swimAddr); err != nil {
		return 2, fmt.Errorf("invalid swim-addr %q: %s", *swimAddr, err)
	}
	if _, _, err := net.SplitHostPort(*swimJoinAddr); err != nil {
		return 2, fmt.Errorf("invalid swim-join-addr %q: %s", *swimJoinAddr, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(wErr, &slog.HandlerOptions{Level: level}))

	err = profile(func() error {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			return err
		}
		httpPort := ln.Addr().(*net.TCPAddr).Port
		srv, err := server.New(server.Config{
			NodeID:         *nodeID,
			Listener:       ln,
			AdvertiseAddr:  fmt.Sprintf("%s:%d", *advertiseHost, httpPort),
			GossipInterval: *gossipInterval,
			Logger:         logger,
		})
		if err != nil {
			return err
		}
		swimConn, err := net.ListenPacket("udp", *swimAddr)
		if err != nil {
			return err
		}
		lnSwim, err := net.Listen("tcp", *swimJoinAddr)
		if err != nil {
			return err
		}
		member, err := swim.New(swim.Config{
			NodeID:              *nodeID,
			AdvertiseHost:       *advertiseHost,
			AppPort:             uint16(httpPort),
			Conn:                swimConn,
			Listener:            lnSwim,
			Seeds:               *swimSeeds,
			ProtocolPeriod:      *swimProtocolPeriod,
			AckTimeout:          *swimAckTimeout,
			SuspicionTimeout:    *swimSuspicionTimeout,
			SubgroupSize:        *swimSubgroupSize,
			DisseminationFactor: *swimDisseminationFactor,
			Notifier:            srv,
			Logger:              logger,
		})
		if err != nil {
			return err
		}

		errs := make(chan error, 2)
		go func() {
			errs <- member.Start(ctx)
		}()
		go func() {
			errs <- srv.Start(ctx)
		}()
		return errors.Join(<-errs, <-errs)
	}, *cpuProfile, *memProfile, *traceProfile)
	if err != nil {
		return 1, err
	}
	return 0, nil
}
