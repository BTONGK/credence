package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/spec"
)

func cmdReview(ctx context.Context, args []string) error {
	fs := newFlags("review", "[session] | <claim> --accept|--edit|--dispute reason|--reject")
	accept := fs.Bool("accept", false, "accept the claim")
	edit := fs.Bool("edit", false, "accept an edited version (with --title/--statement)")
	title := fs.String("title", "", "edited title")
	statement := fs.String("statement", "", "edited statement")
	dispute := fs.String("dispute", "", "dispute with a reason: "+strings.Join(spec.DisputeReasons, ", "))
	reject := fs.Bool("reject", false, "reject the claim")
	note := fs.String("note", "", "note recorded with the decision")
	acceptVerified := fs.Bool("accept-verified", false, "accept every verified claim in the session")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	decision := ""
	switch {
	case *accept:
		decision = spec.ReviewAccepted
	case *edit:
		decision = spec.ReviewEdited
	case *dispute != "":
		decision = spec.ReviewDisputed
	case *reject:
		decision = spec.ReviewRejected
	}
	if len(pos) == 1 && strings.HasPrefix(pos[0], "clm_") {
		if decision == "" {
			return flag2("pass --accept, --edit, --dispute <reason> or --reject")
		}
		row, err := d.Store.GetClaim(pos[0])
		if err != nil {
			return err
		}
		e, err := d.Ledger.Decide(ctx, row.Claim.ID, spec.HumanDecision{Decision: decision, By: humanID(d, row.Claim.SessionID),
			Reason: *dispute, Note: *note, EditedTitle: *title, EditedStatement: *statement})
		if err != nil {
			return err
		}
		fmt.Printf("%s %s %q (ledger #%d)\n", decision, row.Claim.ID, row.Claim.Title, e.Seq)
		return nil
	}
	if *acceptVerified {
		if len(pos) != 1 {
			return flag2("--accept-verified needs a session id")
		}
		s, err := d.Store.FindSession(pos[0])
		if err != nil {
			return err
		}
		n, err := acceptAllVerified(ctx, d, s.ID)
		fmt.Printf("accepted %d verified claims in %s\n", n, s.ID)
		return err
	}
	fi, _ := os.Stdin.Stat()
	if fi == nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errNoTTY
	}
	var sessions []string
	if len(pos) == 1 {
		s, err := d.Store.FindSession(pos[0])
		if err != nil {
			return err
		}
		sessions = []string{s.ID}
	} else {
		rows, err := d.Store.ListSessions(store.SessionFilter{NeedsReview: true, Limit: 100})
		if err != nil {
			return err
		}
		for _, r := range rows {
			sessions = append(sessions, r.ID)
		}
	}
	if len(sessions) == 0 {
		fmt.Println("Nothing to review. 🎉")
		return nil
	}
	return (&tui{d: d, ctx: ctx, sessions: sessions}).run()
}

func humanID(d *server.Daemon, sessionID string) string {
	if d.Cfg.Human != "" {
		return "human:" + strings.ToLower(d.Cfg.Human)
	}
	if s, err := d.Store.GetSession(sessionID); err == nil && s.Human != "" {
		return s.Human
	}
	return "human:" + os.Getenv("USER")
}

func acceptAllVerified(ctx context.Context, d *server.Daemon, sessionID string) (int, error) {
	rows, err := d.Store.SessionClaims(sessionID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		if r.Primary && r.ReviewState == spec.ReviewPending && r.Section == spec.SectionVerified {
			if _, err := d.Ledger.Decide(ctx, r.Claim.ID, spec.HumanDecision{Decision: spec.ReviewAccepted, By: humanID(d, sessionID), Note: "accepted with shift+A"}); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// tui is the keyboard review loop: j/k move, a accept, e edit, d dispute,
// r reject, shift+A accept all verified, v re-verify, n/p session, q quit.
type tui struct {
	d        *server.Daemon
	ctx      context.Context
	sessions []string
	si       int
	cursor   int
	cards    []*view.ClaimCard
	rep      *view.Report
	msg      string
	opened   time.Time
	decided  int
	saved    string
	in       *bufio.Reader
}

func (t *tui) raw(on bool) {
	if on {
		out, err := exec.Command("stty", "-g").CombinedOutput()
		if err == nil && t.saved == "" {
			t.saved = strings.TrimSpace(string(out))
		}
		c := exec.Command("stty", "cbreak", "-echo")
		c.Stdin = os.Stdin
		_ = c.Run()
		fmt.Print("\x1b[?25l")
		return
	}
	args := []string{"sane"}
	if t.saved != "" {
		args = []string{t.saved}
	}
	c := exec.Command("stty", args...)
	c.Stdin = os.Stdin
	_ = c.Run()
	fmt.Print("\x1b[?25h")
}

func (t *tui) load() error {
	rep, err := view.BuildReport(t.d.Store, t.sessions[t.si])
	if err != nil {
		return err
	}
	t.rep = rep
	t.cards = nil
	for _, s := range rep.Sections {
		t.cards = append(t.cards, s.Claims...)
	}
	if t.cursor >= len(t.cards) {
		t.cursor = max(0, len(t.cards)-1)
	}
	return nil
}

func (t *tui) run() error {
	t.in = bufio.NewReader(os.Stdin)
	t.raw(true)
	defer func() {
		t.raw(false)
		fmt.Print("\x1b[2J\x1b[H")
		t.recordTiming()
	}()
	if err := t.load(); err != nil {
		return err
	}
	t.opened = time.Now()
	for {
		t.draw()
		b, err := t.in.ReadByte()
		if err != nil {
			return nil
		}
		t.msg = ""
		switch b {
		case 'q', 3:
			return nil
		case 'j':
			t.move(1)
		case 'k':
			t.move(-1)
		case 0x1b:
			if n, _ := t.in.ReadByte(); n == '[' {
				switch c, _ := t.in.ReadByte(); c {
				case 'A':
					t.move(-1)
				case 'B':
					t.move(1)
				}
			}
		case 'a':
			t.decide(spec.HumanDecision{Decision: spec.ReviewAccepted})
		case 'r':
			t.decide(spec.HumanDecision{Decision: spec.ReviewRejected})
		case 'd':
			t.disputeFlow()
		case 'e':
			t.editFlow()
		case 'A':
			n, err := acceptAllVerified(t.ctx, t.d, t.sessions[t.si])
			t.decided += n
			if err != nil {
				t.msg = red(err.Error())
			} else {
				t.msg = green(fmt.Sprintf("accepted %d verified claims", n))
			}
			_ = t.load()
		case 'v':
			if c := t.current(); c != nil {
				t.msg = "re-verifying " + c.Claim.ID + "…"
				t.draw()
				if _, _, err := t.d.Pipeline.VerifyOne(t.ctx, c.Claim.ID, true); err != nil {
					t.msg = red(err.Error())
				} else {
					t.msg = "re-verified"
				}
				_ = t.load()
			}
		case 'n', 'l':
			if t.si < len(t.sessions)-1 {
				t.recordTiming()
				t.si++
				t.cursor = 0
				_ = t.load()
				t.opened = time.Now()
			}
		case 'p', 'h':
			if t.si > 0 {
				t.recordTiming()
				t.si--
				t.cursor = 0
				_ = t.load()
				t.opened = time.Now()
			}
		}
	}
}

func (t *tui) recordTiming() {
	if t.decided > 0 && t.rep != nil {
		_ = t.d.Store.RecordReviewTiming(t.rep.Session.ID, time.Since(t.opened).Milliseconds(), t.decided, "cli")
	}
	t.decided = 0
}

func (t *tui) move(n int) {
	t.cursor = max(0, min(len(t.cards)-1, t.cursor+n))
}

func (t *tui) current() *view.ClaimCard {
	if t.cursor < 0 || t.cursor >= len(t.cards) {
		return nil
	}
	return t.cards[t.cursor]
}

func (t *tui) decide(hd spec.HumanDecision) {
	c := t.current()
	if c == nil {
		return
	}
	hd.By = humanID(t.d, c.Claim.SessionID)
	if _, err := t.d.Ledger.Decide(t.ctx, c.Claim.ID, hd); err != nil {
		t.msg = red(err.Error())
		return
	}
	t.decided++
	t.msg = green(hd.Decision + ": " + c.DisplayTitle)
	_ = t.load()
	t.move(1)
}

func (t *tui) disputeFlow() {
	c := t.current()
	if c == nil {
		return
	}
	t.msg = "dispute reason: "
	for i, r := range spec.DisputeReasons {
		t.msg += fmt.Sprintf("%d %s  ", i+1, r)
	}
	t.draw()
	b, _ := t.in.ReadByte()
	i := int(b - '1')
	if i < 0 || i >= len(spec.DisputeReasons) {
		t.msg = "dispute cancelled"
		return
	}
	t.decide(spec.HumanDecision{Decision: spec.ReviewDisputed, Reason: spec.DisputeReasons[i]})
}

func (t *tui) editFlow() {
	c := t.current()
	if c == nil {
		return
	}
	t.raw(false)
	fmt.Print("\x1b[2J\x1b[H")
	fmt.Printf("Edit claim %s\n\nTitle:     %s\nStatement: %s\n\n", c.Claim.ID, c.DisplayTitle, c.DisplayStatement)
	rd := bufio.NewReader(os.Stdin)
	fmt.Print("New title (enter to keep): ")
	nt, _ := rd.ReadString('\n')
	fmt.Print("New statement (enter to keep): ")
	ns, _ := rd.ReadString('\n')
	t.raw(true)
	nt, ns = strings.TrimSpace(nt), strings.TrimSpace(ns)
	if nt == "" && ns == "" {
		t.msg = "edit cancelled"
		return
	}
	t.decide(spec.HumanDecision{Decision: spec.ReviewEdited, EditedTitle: nt, EditedStatement: ns})
}

func (t *tui) draw() {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	s := t.rep.Session
	fmt.Fprintf(&b, "%s  %s  %s\n", bold("Credence review"), bold(orDash(s.Title)), dim(fmt.Sprintf("session %d/%d · %s", t.si+1, len(t.sessions), s.ID)))
	fmt.Fprintf(&b, "%s\n", dim(fmt.Sprintf("%s · %s · %s@%s · %s · $%.2f", s.Harness, orDash(s.Model), orDash(s.RepoName), orDash(s.Branch), dur(s.DurationSec), s.CostUSD)))
	fmt.Fprintf(&b, "%s\n", s.Summary)
	i := 0
	for _, sec := range t.rep.Sections {
		if len(sec.Claims) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n", sectionColor(sec.Key, bold(strings.ToUpper(sec.Title))))
		for _, c := range sec.Claims {
			cur := "  "
			line := fmt.Sprintf("%s %s %s%s", statusGlyph(c), c.DisplayTitle, dim(c.Claim.Type), reviewTag(c))
			if i == t.cursor {
				cur = cyan("▶ ")
				line = bold(stripANSI(line))
			}
			fmt.Fprintf(&b, "%s%s\n", cur, line)
			if i == t.cursor {
				fmt.Fprintf(&b, "    %s\n", trunc(c.DisplayStatement, 150))
				for _, ch := range c.Checks {
					mark := yellow("·")
					switch ch.Result {
					case spec.ResultPass:
						mark = green("✓")
					case spec.ResultFail:
						mark = red("✗")
					}
					fmt.Fprintf(&b, "    %s %s\n", mark, trunc(ch.Plain, 150))
				}
				if c.Credit != "" {
					fmt.Fprintf(&b, "    %s\n", dim("credit "+c.Credit))
				}
				if c.SelfConfidence != nil {
					fmt.Fprintf(&b, "    %s\n", dim(fmt.Sprintf("agent confidence %.0f%% · status %s · value %.2f", *c.SelfConfidence*100, c.Status, c.Value)))
				}
				for _, n := range c.Notes {
					if len(c.Checks) == 0 {
						fmt.Fprintf(&b, "    %s\n", dim(trunc(n, 150)))
					}
				}
			}
			i++
		}
	}
	if len(t.cards) == 0 {
		b.WriteString("\nNo claims in this session.\n")
	}
	fmt.Fprintf(&b, "\n%s\n", dim("j/k move · a accept · e edit · d dispute · r reject · A accept all verified · v re-verify · n/p session · q quit"))
	if t.msg != "" {
		fmt.Fprintf(&b, "%s\n", t.msg)
	}
	os.Stdout.WriteString(b.String())
}
