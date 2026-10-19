package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/claudecode"
	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/policy"
)

// harnessNames are the names people know the harnesses by.
var harnessNames = map[string]string{"claude-code": "Claude Code", "codex": "Codex", "cursor": "Cursor"}

// cmdSetup is the guided first run: connect the agents, check the last
// week of sessions, open the reports.
func cmdSetup(ctx context.Context, args []string) error {
	fs := newFlags("setup", "[--yes] [--if-needed] [--since 7d] [--no-service] [--no-open]")
	yes := fs.Bool("yes", false, "answer yes to both questions")
	ifNeeded := fs.Bool("if-needed", false, "keep an existing setup and add only what is new")
	since := fs.String("since", "7d", "how far back the first check looks")
	harness := fs.String("harness", "", "comma-separated harnesses (default: all detected)")
	noService := fs.Bool("no-service", false, "run the daemon without installing a background service")
	noOpen := fs.Bool("no-open", false, "print the report link instead of opening a browser")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if _, err := policy.ParseWindow(*since); err != nil {
		return err
	}
	if _, err := os.Stat(home.Config()); err == nil && *ifNeeded {
		refreshSetup()
		fmt.Println("Credence is set up. Type `credence` to open your reports.")
		return nil
	}
	if !*yes && !isTTY(os.Stdin) {
		return errors.New("setup asks two questions: run `credence setup` in a terminal, or `credence setup --yes`")
	}
	fmt.Println(bold("Set up Credence"))
	fmt.Println("Credence checks what your AI agent says it did against what really happened in the session.")
	fmt.Println("Everything stays on this Mac.")
	fmt.Println()
	targets := detectTargets(*harness, false)
	if len(targets) == 0 {
		fmt.Println("Credence connects to Claude Code, Codex and Cursor. Install one of them, then run `credence setup` again.")
		fmt.Println("Meanwhile, `credence demo` shows a full example report.")
		return nil
	}
	var names []string
	claude := false
	for _, a := range targets {
		names = append(names, harnessName(a.Name()))
		claude = claude || a.Name() == claudecode.Name
	}
	fmt.Printf("Found %s.\n", joinAnd(names))
	what := "Connecting adds capture hooks"
	if claude {
		what += " and a /credence command"
	}
	fmt.Println(what + ", and runs a small background service.")
	if !ask(fmt.Sprintf("Connect Credence to %s?", joinAnd(names)), *yes) {
		fmt.Println("Run `credence setup` whenever you are ready.")
		return nil
	}
	if err := runInit(ctx, initOptions{scope: adapters.ScopeUser, targets: targets, noService: *noService, guided: true}); err != nil {
		return err
	}
	fmt.Println()
	if ask(fmt.Sprintf("Check your sessions from the last %s now?", windowWords(*since)), *yes) {
		if err := firstCheck(ctx, *since); err != nil {
			fmt.Printf("  %s %v\n", yellow("!"), err)
		}
	} else {
		fmt.Println("  New sessions get checked as you work. `credence backfill` checks older ones anytime.")
	}
	fmt.Println()
	if i, err := startBackground(); err == nil {
		url := fmt.Sprintf("%s/#token=%s", i.URL(), i.Token)
		if !*noOpen && openBrowser(url) == nil {
			fmt.Println("Your reports are open in the browser.")
		} else {
			fmt.Println("Your reports: " + url)
		}
	}
	fmt.Println()
	fmt.Println(bold("You're set."))
	for _, n := range names {
		if n == "Claude Code" {
			fmt.Println("  • Use Claude Code as usual. After a reply that makes claims, Credence adds a one-line check.")
			fmt.Println("  • Type /credence in Claude Code to see every claim in the session, with reasons.")
		}
	}
	if !claude {
		fmt.Printf("  • Use %s as usual. Each session's report is ready a few minutes after it goes quiet.\n", joinAnd(names))
	}
	fmt.Println("  • Type `credence` in a terminal to open your reports.")
	return nil
}

// firstCheck brings in recent sessions and waits for their reports.
func firstCheck(ctx context.Context, since string) error {
	i, err := startBackground()
	if err != nil {
		return err
	}
	fmt.Print("  Reading your sessions… ")
	bctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var res server.BackfillResult
	if err := i.Post(bctx, "/api/backfill", map[string]string{"since": since}, &res); err != nil {
		fmt.Println()
		return err
	}
	if res.Queued == 0 {
		fmt.Println("done.")
		fmt.Printf("  Your sessions from the last %s are all in. New sessions get checked as you work.\n", windowWords(since))
		return nil
	}
	fmt.Printf("found %s.\n", plural(res.Queued, "session"))
	deadline := time.Now().Add(3 * time.Minute)
	tty := isTTY(os.Stdout)
	for {
		var st struct {
			Jobs map[string]int `json:"jobs"`
		}
		left := 0
		if err := i.Get(ctx, "/api/status", &st); err == nil {
			left = min(res.Queued, st.Jobs["pending"]+st.Jobs["running"])
		}
		if tty {
			fmt.Printf("\r  Checking claims… %d of %d sessions done", res.Queued-left, res.Queued)
		}
		if left == 0 {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			fmt.Printf("\n  The other %s keep checking in the background.\n", plural(left, "session"))
			return nil
		}
		time.Sleep(time.Second)
	}
	if tty {
		fmt.Println()
	}
	// Sum the verdicts across the sessions just checked.
	dd, _ := policy.ParseWindow(since)
	var list struct {
		Sessions []view.SessionItem `json:"sessions"`
	}
	day := time.Now().Add(-dd).Format("2006-01-02")
	if err := i.Get(ctx, "/api/sessions?since="+day, &list); err != nil {
		return nil
	}
	counts := map[string]int{}
	claims, withClaims := 0, 0
	for _, s := range list.Sessions {
		if s.Claims > 0 {
			withClaims++
		}
		for k, n := range s.Tally {
			counts[k] += n
			claims += n
		}
	}
	if claims == 0 {
		fmt.Printf("  %s %s checked. Their claims appear in the reports as Credence finds them.\n", green("✓"), plural(res.Queued, "session"))
		return nil
	}
	in := ""
	if withClaims < res.Queued {
		in = fmt.Sprintf(" in %d of them", withClaims)
	}
	fmt.Printf("  %s %s checked: %s%s (%s).\n", green("✓"), plural(res.Queued, "session"), plural(claims, "claim"), in, tally(counts))
	return nil
}

// refreshSetup adds what a newer Credence brings to an existing setup:
// the /credence skill for people whose Claude Code hooks are in place.
func refreshSetup() {
	a, ok := adapters.Get(claudecode.Name)
	if !ok || claudecode.SkillInstalled() {
		return
	}
	for _, h := range a.Doctor(adapters.InstallOptions{Scope: adapters.ScopeUser, Binary: exe()}) {
		if h.Check == "claude-code hooks registered" && !h.OK {
			return
		}
	}
	if _, err := claudecode.InstallSkill(exe(), false); err == nil {
		fmt.Printf("%s added the /credence skill to Claude Code\n", green("✓"))
	}
}

var stdin = bufio.NewReader(os.Stdin)

// ask puts a yes-or-no question whose answer defaults to yes.
func ask(q string, yes bool) bool {
	fmt.Printf("%s %s ", bold(q), dim("[Y/n]"))
	if yes {
		fmt.Println("yes")
		return true
	}
	line, err := stdin.ReadString('\n')
	if err != nil && line == "" {
		fmt.Println()
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	}
	return false
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func harnessName(id string) string {
	if n, ok := harnessNames[id]; ok {
		return n
	}
	return id
}

// joinAnd joins names as people write them: "A, B and C".
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// windowWords turns "7d" into "7 days".
func windowWords(w string) string {
	if n := strings.TrimSuffix(w, "d"); n != w {
		if n == "1" {
			return "day"
		}
		return n + " days"
	}
	return w
}
