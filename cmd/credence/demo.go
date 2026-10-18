package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/demo"
	"github.com/BTONGK/credence/internal/home"
)

func cmdDemo(ctx context.Context, args []string) error {
	fs := newFlags("demo", "[--dir path] [--port n] [--no-open] [--no-serve]")
	dir := fs.String("dir", filepath.Join(home.UserHome(), "credence-demo"), "where to create the demo repo and its separate Credence home")
	port := fs.Int("port", 7172, "UI port for the demo")
	noOpen := fs.Bool("no-open", false, "do not open a browser")
	noServe := fs.Bool("no-serve", false, "build the demo and exit")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	abs, _ = filepath.EvalSymlinks(abs)
	// The demo never touches your real ~/.credence.
	demoHome := filepath.Join(abs, "home")
	os.Setenv("CREDENCE_HOME", demoHome)
	os.Setenv("CREDENCE_NO_AUTOSTART", "1")
	var sid string
	if _, err := os.Stat(filepath.Join(abs, "scheduler")); err == nil {
		fmt.Printf("Demo already built in %s (delete it to rebuild).\n", abs)
		d, err := open()
		if err != nil {
			return err
		}
		rows, _ := d.Store.ListSessions(storeFilterAll())
		if len(rows) > 0 {
			sid = rows[0].ID
		}
		d.Close()
	} else {
		fmt.Println(bold("Building the example from the product doc:"), "Maya asks Claude Code to fix a flaky scheduler test.")
		fmt.Println(dim("  creating a git repo with a real race condition and replaying the agent's session (every command really runs)…"))
		sc, err := demo.Build(ctx, abs, time.Now().Add(-3*time.Minute))
		if err != nil {
			return err
		}
		d, err := server.Open(server.Options{Log: io.Discard})
		if err != nil {
			return err
		}
		sid, err = demo.Ingest(ctx, d.Store, d.Cfg, sc, nil)
		if err != nil {
			d.Close()
			return err
		}
		fmt.Println(dim("  verifying claims: re-running tests 50× before and after the fix in clean worktrees…"))
		start := time.Now()
		sum, err := d.Pipeline.Report(ctx, sid, pipeline.Options{Final: true})
		if err != nil {
			d.Close()
			return err
		}
		fmt.Printf("  %s %s (%s)\n", green("✓"), view.SummaryLine(sum.Claims, sum.Sections), time.Since(start).Round(100*time.Millisecond))
		rep, err := view.BuildReport(d.Store, sid)
		if err == nil {
			fmt.Println()
			printReport(rep)
		}
		d.Close()
		fmt.Printf("\nRepo: %s\nCredence home for the demo: %s\n", sc.Repo, demoHome)
	}
	if *noServe {
		fmt.Printf("\nReview it in the terminal:  CREDENCE_HOME=%s credence review\n", demoHome)
		return nil
	}
	fmt.Println()
	return server.Run(ctx, server.Options{Port: *port, NoScan: true, Log: io.Discard, Ready: func(i *server.Info) {
		url := fmt.Sprintf("%s/#token=%s", i.URL(), i.Token)
		if sid != "" {
			url = fmt.Sprintf("%s/sessions/%s#token=%s", i.URL(), sid, i.Token)
		}
		fmt.Printf("Demo UI: %s\n%s\n", i.URL(), dim("Press Ctrl-C to stop. In the UI: j/k move · a accept · e edit · d dispute · shift+A accept all verified."))
		if !*noOpen {
			_ = openBrowser(url)
		} else {
			fmt.Println(url)
		}
	}})
}

func storeFilterAll() store.SessionFilter { return store.SessionFilter{Limit: 1} }
