package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/protocol"
	"github.com/lbagic/regrow/internal/scanner"
)

// runEngine serves the engine protocol on the process's stdin and
// stdout until stdin ends or a termination signal arrives.
func runEngine(host engine.Host, catalog []engine.Rule, opts options) error {
	keepRunningOnEPIPE()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal asks for a clean stop; a second one kills.
	go func() {
		<-ctx.Done()
		stop()
	}()
	flags, err := cleanFlags(opts)
	if err != nil {
		return err
	}
	return serveEngine(ctx, host, catalog, flags, os.Stdin, os.Stdout)
}

// keepRunningOnEPIPE makes a write to a closed stdout fail with EPIPE
// instead of killing the process: an execute in flight still has to
// journal its current action. Notify, unlike Ignore, leaves child
// processes the default disposition.
func keepRunningOnEPIPE() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
}

func serveEngine(ctx context.Context, host engine.Host, catalog []engine.Rule, flags []string, in io.Reader, out io.Writer) error {
	return newEngineServer(host, catalog, flags).Serve(ctx, in, out)
}

// newEngineServer is what `regrow engine` and `regrow scan --json`
// both speak through.
func newEngineServer(host engine.Host, catalog []engine.Rule, flags []string) *protocol.Server {
	return &protocol.Server{
		Version:    version,
		Host:       host,
		Catalog:    catalog,
		CleanFlags: flags,
		Scan:       scanStream(host),
		Account:    engine.Account,
		NewExecutor: func(runID string) (*executor.Executor, func(), error) {
			return newRunExecutor(host, runID, dockerStream)
		},
	}
}

// cleanFlags are the flags that make `regrow clean` in Terminal load
// the engine's catalog. The rules dir is made absolute: Terminal opens
// in another directory.
func cleanFlags(opts options) ([]string, error) {
	var flags []string
	if opts.rulesDir != "" {
		dir, err := filepath.Abs(opts.rulesDir)
		if err != nil {
			return nil, err
		}
		flags = append(flags, "--rules-dir", dir)
	}
	if opts.betaRules {
		flags = append(flags, "--beta-rules")
	}
	return flags, nil
}

// dockerStream runs the volume export with stderr captured, so a
// failed export journals docker's reason rather than the engine's
// stderr getting it.
func dockerStream(ctx context.Context, args []string, stdout io.Writer) error {
	return protocol.RunNativeTo(ctx, append([]string{"docker"}, args...), stdout)
}

// scanStream builds a scanner per scan: tool providers keep one
// snapshot per scanner, and a reused one would hand a rescan the
// previous scan's docker state.
func scanStream(host engine.Host) protocol.ScanFunc {
	return func(ctx context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		scanner.New(host).ScanStream(ctx, rules, emit)
	}
}
