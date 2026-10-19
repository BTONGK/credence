package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/quick"
	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/home"
)

// verdictOrder lists verdicts from the ones that need a look to the ones
// that are settled.
var verdictOrder = []string{view.VerdictContradicted, view.VerdictPartly, view.VerdictUnchecked, view.VerdictPending, view.VerdictBacked}

func cmdCheck(ctx context.Context, args []string) error {
	fs := newFlags("check", "[--harness h] [--session id] [--cwd dir] [--json]")
	harness := fs.String("harness", "claude-code", "the agent harness")
	session := fs.String("session", "", "the harness's session id (default: the newest session in --cwd)")
	transcript := fs.String("transcript", "", "the session's transcript file")
	cwd := fs.String("cwd", "", "the session's folder (default: this folder)")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	if _, err := os.Stat(home.Config()); os.IsNotExist(err) {
		fmt.Println("Credence is ready to set up. Run `credence` in a terminal and answer two questions; checks start with the next session.")
		return nil
	}
	i, err := startBackground()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res quick.Result
	req := server.CheckRequest{Harness: *harness, Session: *session, Transcript: *transcript, Cwd: *cwd}
	if err := i.Post(ctx, "/api/check", req, &res); err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	fmt.Print(checkText(&res))
	return nil
}

// checkText is the plain report `credence check` prints for the /credence
// skill: the claims that need a look first, then the totals.
func checkText(res *quick.Result) string {
	var b strings.Builder
	if len(res.Claims) == 0 {
		b.WriteString("Credence found no claims to check in this session yet. Claims are statements like \"I fixed the login bug\" or \"all tests pass\"; they show up here as the agent makes them.\n")
		b.WriteString("Full report: run `credence` in a terminal.\n")
		return b.String()
	}
	title := ""
	if res.Title != "" {
		title = " (" + trunc(res.Title, 60) + ")"
	}
	fmt.Fprintf(&b, "Credence checked %s in this session%s against the session record.\n\n", plural(len(res.Claims), "claim"), title)
	const limit = 25
	shown := 0
	for _, v := range verdictOrder {
		for _, c := range res.Claims {
			if c.Verdict != v {
				continue
			}
			if shown == limit {
				break
			}
			shown++
			meta := verdicts[v]
			fmt.Fprintf(&b, "%s %s: “%s”\n", meta.mark, meta.label, strings.ReplaceAll(c.Statement, "`", ""))
			if c.Reason != "" {
				fmt.Fprintf(&b, "  %s\n", c.Reason)
			}
		}
	}
	if more := len(res.Claims) - shown; more > 0 {
		fmt.Fprintf(&b, "\n%s more in the full report.\n", plural(more, "claim"))
	}
	fmt.Fprintf(&b, "\nTotals: %s.\n", tally(res.Counts))
	b.WriteString("Full report: run `credence` in a terminal.\n")
	return b.String()
}

// tally words verdict counts: "3 backed by evidence, 1 contradicted".
func tally(counts map[string]int) string {
	var parts []string
	for _, k := range []string{view.VerdictBacked, view.VerdictPartly, view.VerdictUnchecked, view.VerdictContradicted, view.VerdictPending} {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], strings.ToLower(verdicts[k].label)))
		}
	}
	return strings.Join(parts, ", ")
}

// stopInput is the part of Claude Code's Stop hook input the note uses.
type stopInput struct {
	Event      string `json:"hook_event_name"`
	Session    string `json:"session_id"`
	Transcript string `json:"transcript_path"`
	Cwd        string `json:"cwd"`
	Active     bool   `json:"stop_hook_active"`
	Reply      string `json:"last_assistant_message"`
}

// replyNote prints the one-line check Claude Code shows after a reply that
// makes claims. It stays quiet on any problem: the hook always succeeds.
func replyNote(harness string, input []byte) {
	defer func() { _ = recover() }()
	if harness != "claude-code" || os.Getenv("CREDENCE_INTERNAL") != "" {
		return
	}
	var in stopInput
	if json.Unmarshal(input, &in) != nil || in.Event != "Stop" || in.Active || strings.TrimSpace(in.Reply) == "" {
		return
	}
	if _, err := os.Stat(home.Config()); err != nil || !config.Load().ReplyNotes() {
		return
	}
	i, err := server.ReadInfo()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var res quick.Result
	req := server.CheckRequest{Harness: harness, Session: in.Session, Transcript: in.Transcript, Cwd: in.Cwd, Reply: in.Reply}
	if i.Post(ctx, "/api/check", req, &res) != nil {
		return
	}
	if msg := noteText(&res); msg != "" {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"systemMessage": msg})
	}
}

// noteText is the reply note: the counts, then the claim most worth a look.
func noteText(res *quick.Result) string {
	n := len(res.Claims)
	if n == 0 {
		return ""
	}
	msg := "Credence: "
	if res.Counts[view.VerdictBacked] == n {
		if n == 1 {
			msg += "1 claim in this reply, backed by evidence."
		} else {
			msg += fmt.Sprintf("%d claims in this reply, all backed by evidence.", n)
		}
		return msg + " Type /credence for the reasons."
	}
	msg += fmt.Sprintf("%s in this reply: %s.", plural(n, "claim"), tally(res.Counts))
	for _, v := range verdictOrder[:3] {
		for _, c := range res.Claims {
			if c.Verdict == v {
				msg += fmt.Sprintf(" %s “%s” %s", verdicts[v].mark, trunc(strings.ReplaceAll(c.Statement, "`", ""), 90), trunc(c.Reason, 160))
				return msg + " Type /credence for every claim."
			}
		}
	}
	return msg + " Type /credence for every claim."
}
