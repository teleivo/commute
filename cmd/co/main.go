package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"syscall"

	"github.com/teleivo/commute/internal/server"
	"github.com/teleivo/commute/internal/version"
)

// errFlagParse is a sentinel error indicating flag parsing failed.
// The flag package already printed the error, so main should not print again.
var errFlagParse = errors.New("flag parse error")

func main() {
	code, err := run(os.Args, os.Stdin, os.Stdout, os.Stderr)
	if err != nil && err != errFlagParse {
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}
	os.Exit(code)
}

func run(args []string, r io.Reader, w io.Writer, wErr io.Writer) (int, error) {
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
	port := flags.String("port", "0", "HTTP server port (0 for a random available port)")
	nodeID := flags.String("nodeid", "", "unique node identifier (required)")
	debug := flags.Bool("debug", false, "enable debug logging")
	cpuProfile := flags.String("cpuprofile", "", "write cpu profile to `file`")
	memProfile := flags.String("memprofile", "", "write memory profile to `file`")
	traceProfile := flags.String("trace", "", "write execution trace to `file`")

	err := flags.Parse(args)
	if err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, errFlagParse
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	err = profile(func() error {
		srv, err := server.New(server.Config{
			NodeID: *nodeID,
			Port:   *port,
			Debug:  *debug,
			Stderr: os.Stderr,
		})
		if err != nil {
			return err
		}
		return srv.Start(ctx)
	}, *cpuProfile, *memProfile, *traceProfile)
	if err != nil {
		return 1, err
	}
	return 0, nil
}
