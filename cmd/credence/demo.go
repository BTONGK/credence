package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/demo"
	"github.com/BTONGK/credence/internal/home"
)

func cmdDemo(ctx context.Context, args []string) error {
	fs := newFlags("demo", "[--dir path] [--port n] [--no-open] [--no-serve] [--ended 3m]")
	dir := fs.String("dir", filepath.Join(home.UserHome(), "credence-demo"), "where to create the example website and its separate Credence home")
	port := fs.Int("port", 7172, "UI port for the demo")
	noOpen := fs.Bool("no-open", false, "print the report link and leave the browser closed")
	noServe := fs.Bool("no-serve", false, "build the demo and exit")
	ended := fs.Duration("ended", 3*time.Minute, "how long ago the example session ended")
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
	if err := clearDemo(abs); err != nil {
		return err
	}
	// The demo keeps its own Credence home, separate from ~/.credence.
	demoHome := filepath.Join(abs, "home")
	os.Setenv("CREDENCE_HOME", demoHome)
	os.Setenv("CREDENCE_NO_AUTOSTART", "1")
	// The example website stands alone, even inside another repository.
	os.Setenv("GIT_CEILING_DIRECTORIES", abs)

	sc, err := demo.Build(ctx, abs, time.Now().Add(-*ended))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(abs, demoMarker), []byte("Built by `credence demo`; it rebuilds this folder on every run.\n"), 0o644); err != nil {
		return err
	}
	d, err := server.Open(server.Options{Log: io.Discard})
	if err != nil {
		return err
	}
	sid, err := demo.Ingest(ctx, d.Store, d.Cfg, sc, nil)
	if err == nil {
		_, err = d.Pipeline.Report(ctx, sid, pipeline.Options{Final: true})
	}
	var rep *view.Report
	if err == nil {
		rep, err = view.BuildReport(d.Store, sid)
	}
	var cards []*view.ClaimCard
	if err == nil {
		cards = demo.Claims(d.Store, rep)
	}
	d.Close()
	if err != nil {
		return err
	}
	printDemo(cards)

	if *noServe {
		fmt.Printf("\nThe example website is in %s\nBrowse the report in the terminal:  CREDENCE_HOME=%s credence review\n", sc.Repo, demoHome)
		return nil
	}
	return server.Run(ctx, server.Options{Port: *port, NoScan: true, Log: io.Discard, Ready: func(i *server.Info) {
		url := fmt.Sprintf("%s/sessions/%s#token=%s", i.URL(), sid, i.Token)
		if *noOpen || openBrowser(url) != nil {
			fmt.Printf("\nOpen the full report:  %s\n", url)
		} else {
			fmt.Printf("\nThe full report is open in your browser at %s\n", i.URL())
		}
		fmt.Println("It shows every claim with its verdict, plus the whole conversation behind it.")
		fmt.Println()
		fmt.Println("Ready for your own sessions? Run " + bold("credence setup") + ": two questions, then your own reports open.")
		fmt.Println(dim("Press Ctrl-C to close the demo."))
	}})
}

const demoMarker = ".credence-demo"

// clearDemo empties a folder an earlier demo built, so every run starts
// fresh. Folders holding anything else are left to the user.
func clearDemo(dir string) error {
	owned := []string{"home", "claude", "sunrise-bakery", "scheduler"}
	_, marked := os.Stat(filepath.Join(dir, demoMarker))
	_, older := os.Stat(filepath.Join(dir, "scheduler")) // the first demo, before the marker
	if marked == nil || older == nil {
		for _, p := range append(owned, demoMarker) {
			if err := os.RemoveAll(filepath.Join(dir, p)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, p := range owned {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			return fmt.Errorf("%s already holds %s; pick an empty folder with --dir", dir, p)
		}
	}
	return nil
}

var verdicts = map[string]struct {
	mark  string
	label string
	paint func(string) string
}{
	view.VerdictBacked:       {"✓", "Backed by evidence", green},
	view.VerdictPartly:       {"◐", "Partly backed", yellow},
	view.VerdictUnchecked:    {"?", "Yours to confirm", func(s string) string { return s }},
	view.VerdictContradicted: {"✗", "Contradicted", red},
	view.VerdictPending:      {"…", "Checking", dim},
}

func printDemo(cards []*view.ClaimCard) {
	fmt.Println()
	fmt.Println(bold("Credence demo"))
	fmt.Println()
	fmt.Println("Sam runs a small bakery and asked Claude Code to update the bakery's website:")
	fmt.Println("holiday hours, a phone-friendly layout, a cake order form, and the hours on Google.")
	fmt.Println("Here is everything the agent said it did, checked against what really happened.")
	fmt.Println()
	counts := map[string]int{}
	for _, c := range cards {
		v, ok := verdicts[c.Verdict]
		if !ok {
			v = verdicts[view.VerdictUnchecked]
		}
		counts[c.Verdict]++
		said := strings.ReplaceAll(c.Claim.Statement, "`", "")
		fmt.Printf("  %s %s  %s\n", v.paint(v.mark), v.paint(pad(v.label, 18)), bold("“"+said+"”"))
		for _, line := range wrapText(c.Reason, 56) {
			fmt.Printf("  %s  %s\n", strings.Repeat(" ", 20), dim(line))
		}
	}
	var parts []string
	for _, k := range []string{view.VerdictBacked, view.VerdictPartly, view.VerdictUnchecked, view.VerdictContradicted} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], strings.ToLower(verdicts[k].label)))
		}
	}
	fmt.Printf("\nThe agent made %d claims: %s.\n", len(cards), strings.Join(parts, ", "))
}

func wrapText(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
