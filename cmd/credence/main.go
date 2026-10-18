// Command credence turns every AI agent session into a credit report:
// agents propose credit, evidence decides.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/BTONGK/credence/adapters/claudecode"
	_ "github.com/BTONGK/credence/adapters/codex"
	_ "github.com/BTONGK/credence/adapters/cursor"
	_ "github.com/BTONGK/credence/adapters/generic"
	"github.com/BTONGK/credence/capture"
	"github.com/BTONGK/credence/mcp"
	"github.com/BTONGK/credence/spec"
)

type command struct {
	name    string
	args    string
	summary string
	run     func(ctx context.Context, args []string) error
	hidden  bool
}

var commands []command

func init() {
	commands = []command{
		{"init", "[--scope user|project|local] [--harness h,...] [--no-mcp] [--no-service] [--dry-run] [--yes]", "install capture hooks and the credence_claim MCP tool", cmdInit, false},
		{"doctor", "[--json]", "check capture, daemon, keys, ledger and model setup", cmdDoctor, false},
		{"sessions", "[--needs-review] [--repo r] [--harness h] [--limit n] [--json]", "list captured sessions, newest first", cmdSessions, false},
		{"show", "<session|claim> [--events] [--json]", "show a session's credit report or one claim card", cmdShow, false},
		{"review", "[session] | <claim> --accept|--edit|--dispute reason|--reject", "review claims (keyboard: j/k a e d r shift+A q)", cmdReview, false},
		{"verify", "<claim> [--rerun]", "re-run verification for a claim", cmdVerify, false},
		{"report", "<session> [--final]", "rebuild a session's report now", cmdReport, false},
		{"ledger", "[--by agent|repo|harness|model] [--entries] [--verify [file]] [--json]", "reputation, calibration and the signed ledger", cmdLedger, false},
		{"export", "--format json|csv [--out file] [--share-person]", "export the signed ledger", cmdExport, false},
		{"ui", "[--no-open]", "open the local web UI", cmdUI, false},
		{"demo", "[--dir path] [--port n] [--no-open]", "open an example report: a bakery website update", cmdDemo, false},
		{"backfill", "[--since 30d] [--harness h]", "capture older transcripts", cmdBackfill, false},
		{"config", "[show | get key | set key value]", "view or change ~/.credence/config.yaml", cmdConfig, false},
		{"contributors", "[--since 30d] [--format md|json] [--release name]", "credits for CONTRIBUTORS or release notes", cmdContributors, false},
		{"pr-comment", "[--pr n] [--repo owner/name] [--base ref] [--trusted-keys ids] [--dry-run]", "post verified claims on a pull request", cmdPRComment, false},
		{"plugin", "list | check <name> | check -- <cmd…> | add <name> [-- <cmd…>] | remove <name>", "verifier plugins: registry, conformance checks, install", cmdPlugin, false},
		{"team", "rollup <export.json|dir>… [--notes repo] [--by agent|repo|harness|model|person]", "aggregate teammates' signed ledgers (opt-in, no transcripts)", cmdTeam, false},
		{"import", "agent-trace|git-ai|entire|otlp|transcript <path> [--repo dir]", "import attribution or sessions from other tools", cmdImport, false},
		{"daemon", "[--port n]", "run credenced in the foreground", cmdDaemon, false},
		{"start", "", "start the daemon in the background", cmdStart, false},
		{"stop", "", "stop the background daemon", cmdStop, false},
		{"uninstall", "[--purge] [--yes]", "remove hooks, MCP registration and the service", cmdUninstall, false},
		{"version", "", "print the version", cmdVersion, false},
		{"hook", "<harness>", "capture hook (called by harnesses)", cmdHook, true},
		{"mcp", "", "stdio MCP server exposing credence_claim", cmdMCP, true},
	}
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		return
	}
	name := os.Args[1]
	if name == "hook" {
		// The capture path: no network, no database, always exit 0.
		h := ""
		if len(os.Args) > 2 {
			h = os.Args[2]
		}
		_ = capture.Hook(h, os.Stdin, time.Now())
		autostart()
		os.Exit(0)
	}
	if name == "mcp" {
		if err := mcp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "credence mcp:", err)
		}
		return
	}
	if name == "-h" || name == "--help" || name == "help" {
		usage(os.Stdout)
		return
	}
	if name == "-v" || name == "--version" {
		name = "version"
	}
	for _, c := range commands {
		if c.name == name {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			err := c.run(ctx, os.Args[2:])
			stop()
			if err != nil {
				if errors.Is(err, flag.ErrHelp) {
					os.Exit(0)
				}
				fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(2)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "credence %s — agents propose credit; evidence decides.\n\nUsage:\n", spec.Version)
	cs := append([]command(nil), commands...)
	sort.SliceStable(cs, func(i, j int) bool { return false })
	for _, c := range cs {
		if c.hidden {
			continue
		}
		fmt.Fprintf(w, "  credence %-13s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\nRun `credence <command> -h` for flags. Data lives in ~/.credence (override with CREDENCE_HOME).")
}

func newFlags(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: credence %s %s\n", name, args)
		fs.PrintDefaults()
	}
	return fs
}

// parse handles flags anywhere in args (Go's flag stops at the first positional).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdVersion(ctx context.Context, args []string) error {
	fmt.Printf("credence %s (spec %s)\n", spec.Version, spec.ClaimSpec)
	return nil
}

func cmdHook(ctx context.Context, args []string) error { return nil }
func cmdMCP(ctx context.Context, args []string) error  { return nil }

func exe() string {
	p, err := os.Executable()
	if err != nil {
		return "credence"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ---- terminal formatting ----

var color = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func sgr(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return sgr("1", s) }
func dim(s string) string    { return sgr("2", s) }
func red(s string) string    { return sgr("31", s) }
func green(s string) string  { return sgr("32", s) }
func yellow(s string) string { return sgr("33", s) }
func blue(s string) string   { return sgr("34", s) }
func cyan(s string) string   { return sgr("36", s) }

func sectionColor(section, s string) string {
	switch section {
	case spec.SectionVerified:
		return green(s)
	case spec.SectionContradicted:
		return red(s)
	case spec.SectionUnverified:
		return yellow(s)
	case spec.SectionAuditor:
		return blue(s)
	}
	return dim(s)
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("Jan 2")
}

func dur(sec int) string {
	switch {
	case sec <= 0:
		return "-"
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%dh%02dm", sec/3600, (sec%3600)/60)
}

func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	l := len([]rune(stripANSI(s)))
	if l >= n {
		return s
	}
	return s + strings.Repeat(" ", n-l)
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in && r == 'm':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func confirm(prompt string) bool {
	fmt.Print(prompt + " [y/N] ")
	var ans string
	_, _ = fmt.Scanln(&ans)
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}
