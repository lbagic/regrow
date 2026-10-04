package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lbagic/regrow/internal/engine"
	"github.com/lbagic/regrow/internal/executor"
	"github.com/lbagic/regrow/internal/protocol"
	"github.com/lbagic/regrow/internal/scanner"
)

// runEngine serves the engine protocol on the process's stdin and
// stdout until stdin ends or a termination signal arrives.
func runEngine(host engine.Host, catalog []engine.Rule) error {
	// A write to a closed stdout must fail, not kill the process: an
	// execute in flight still has to journal its current action.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal asks for a clean stop; a second one kills.
	go func() {
		<-ctx.Done()
		stop()
	}()
	return serveEngine(ctx, host, catalog, os.Stdin, os.Stdout)
}

func serveEngine(ctx context.Context, host engine.Host, catalog []engine.Rule, in io.Reader, out io.Writer) error {
	srv := &protocol.Server{
		Version: version,
		Host:    host,
		Catalog: catalog,
		Scan:    scanStream(host),
		NewExecutor: func(runID string) (*executor.Executor, func(), error) {
			return newRunExecutor(host, runID)
		},
	}
	return srv.Serve(ctx, in, out)
}

// scanStream fills the engine's streaming scan seam from the
// all-at-once Scan: every finding arrives when the whole scan is
// done, with no per-rule time.
func scanStream(host engine.Host) protocol.ScanFunc {
	return func(ctx context.Context, rules []engine.Rule, emit func(int, engine.Finding, time.Duration)) {
		for i, f := range scanner.New(host).Scan(ctx, rules) {
			emit(i, f, 0)
		}
	}
}
