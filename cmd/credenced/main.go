// Command credenced is the Credence daemon. `credence daemon` runs the same
// code; this binary exists for service managers and packaging.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/BTONGK/credence/adapters/claudecode"
	_ "github.com/BTONGK/credence/adapters/codex"
	_ "github.com/BTONGK/credence/adapters/cursor"
	_ "github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/daemon/server"
)

func main() {
	port := flag.Int("port", 0, "listen port (default from config, 7171)")
	verbose := flag.Bool("v", false, "log to stderr")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := server.Options{Port: *port}
	if *verbose {
		opts.Log = os.Stderr
	}
	if err := server.Run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "credenced:", err)
		os.Exit(1)
	}
}
