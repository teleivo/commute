package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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
	gossipInterval := flags.Duration("gossipinterval", 5*time.Second, "how often to push state to a random peer")
	n := flags.Int("nodes", 4, "number of nodes")
	count := flags.Int("count", 1_000_000, "total number of increments across all nodes")

	err := flags.Parse(args)
	if err != nil {
		if err == flag.ErrHelp {
			return 0, nil
		}
		return 2, errFlagParse
	}

	if *count%*n != 0 {
		return 2, fmt.Errorf("count %d must be divisible by nodes %d", *count, *n)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	_ = gossipInterval
	type Node struct {
		nodeID int
		addr   string
		peers  []string
	}
	nodes := make([]Node, *n)
	for i := range *n {
		addr := pickPort()
		nodes[i] = Node{
			nodeID: i,
			addr:   addr,
		}
	}
	all := make([]string, *n)
	for i := range *n {
		all[i] = nodes[i].addr
	}
	for i := range *n {
		peers := make([]string, *n-1)
		copy(peers, all[:i])
		copy(peers[i:], all[i+1:])
		nodes[i].peers = peers
	}
	fmt.Println("nodes", nodes)
	cmds := make([]*exec.Cmd, *n)
	for i := range *n {
		// TODO fix passing interval
		// cmds[i] := exec.CommandContext(ctx, "co", "--nodeid", strconv.Itoa(nodes[i].nodeID), "--addr", nodes[i].addr, "--advertise-addr", nodes[i].addr, "--gossipinterval", *gossipInterval)
		cmds[i] = exec.CommandContext(ctx, "co", "server",
			"--nodeid", strconv.Itoa(nodes[i].nodeID),
			"--addr", nodes[i].addr,
			"--advertise-addr", nodes[i].addr,
			"--peers", strings.Join(nodes[i].peers, ","),
		)
		// TODO deal with errors
		// cmds[i].Stdout = os.Stdout
		// cmds[i].Stderr = os.Stderr
		err := cmds[i].Start()
		if err != nil {
			panic(err)
		}
	}

	time.Sleep(3 * time.Second)

	var wg sync.WaitGroup
	wg.Add(*n)

	// TODO setup slogger
	client := http.Client{}

	for i := range nodes {
		go func(node Node, target int) {
			defer wg.Done()

			for range target {
				// TODO error handling: return goroutine on first error or just log?
				// TODO implement pacing?
				b := `{"increment":1}`
				reqCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
				req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, "http://"+node.addr+"/counters/count", bytes.NewReader([]byte(b)))
				if err != nil {
					cancel()
					panic(err)
				}
				req.Header.Add("Content-Type", "application/json")
				resp, err := client.Do(req)
				cancel()
				if err != nil {
					// srv.logger.Warn("failed to gossip state", "peer", peer, "error", err)
					// continue
					panic(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					// srv.logger.Warn("gossip rejected", "peer", peer, "status", resp.StatusCode)
					// continue
					panic("error")
				}
			}
		}(nodes[i], *count / *n)
	}

	wg.Wait()
	// TODO wait until they also converge to the count?
	fmt.Println("counted to", count)
	// TODO create http server that exposes the overall value and individual increments per node?
	// TODO serve html that visualizes the above

	return 0, nil
}

// pickPort returns a free TCP address on localhost. There is a TOCTOU race between closing the
// listener here and the co process binding the port. Acceptable for a local demo.
func pickPort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = l.Close()
	}()
	addr := l.Addr().String()
	return addr
}
