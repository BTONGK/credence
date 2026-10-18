package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/spec"
)

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func cmdSessions(ctx context.Context, args []string) error {
	fs := newFlags("sessions", "[--needs-review] [--repo r] [--harness h] [--limit n] [--json]")
	needs := fs.Bool("needs-review", false, "only sessions with claims awaiting review")
	repo := fs.String("repo", "", "filter by repo name or path")
	harness := fs.String("harness", "", "filter by harness")
	cfgID := fs.String("agent-config", "", "filter by agent config (cfg_xxxx)")
	q := fs.String("q", "", "search titles, repos and branches")
	limit := fs.Int("limit", 30, "maximum sessions")
	asJSON := fs.Bool("json", false, "JSON output")
	all := fs.Bool("all", false, "include sessions with nothing to claim")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	items, err := view.Inbox(d.Store, store.SessionFilter{NeedsReview: *needs, Repo: *repo, Harness: *harness, AgentConfig: *cfgID, Query: *q, Limit: *limit})
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(items)
	}
	if len(items) == 0 {
		fmt.Println("No sessions captured yet. Run `credence init`, then use your agent as usual (or try `credence demo`).")
		return nil
	}
	muted := 0
	for _, it := range items {
		if it.Muted && !*all {
			muted++
			continue
		}
		title := it.Title
		if title == "" {
			title = "(untitled)"
		}
		review := ""
		if it.Pending > 0 {
			review = yellow(fmt.Sprintf(" · %d to review", it.Pending))
		}
		fmt.Printf("%s  %s  %s\n", bold(it.ID), trunc(title, 70), dim(ago(it.LastActivity)))
		fmt.Printf("    %s · %s · %s@%s · %s · $%.2f\n", it.Harness, orDash(it.Model), orDash(it.RepoName), orDash(it.Branch), dur(it.DurationSec), it.CostUSD)
		fmt.Printf("    %s%s%s\n", it.Summary, review, stateTag(it.SessionRow))
	}
	if muted > 0 {
		fmt.Println(dim(fmt.Sprintf("\n%d sessions with nothing to claim hidden (--all to show)", muted)))
	}
	return nil
}

func stateTag(s *store.SessionRow) string {
	switch {
	case s.State != "closed" && s.ReportState == "provisional":
		return dim(" · provisional (session open)")
	case s.State != "closed":
		return dim(" · in progress")
	}
	for _, f := range s.Flags {
		if f == "transcript_rewritten" {
			return red(" · transcript changed after capture")
		}
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func cmdShow(ctx context.Context, args []string) error {
	fs := newFlags("show", "[session|claim] [--events] [--json]")
	asJSON := fs.Bool("json", false, "JSON output")
	events := fs.Bool("events", false, "list raw events instead of episodes")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		fs.Usage()
		return flag2("expected one session or claim id")
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	// With no id, show the most recent session.
	ref := ""
	if len(pos) == 1 {
		ref = pos[0]
	} else {
		rows, err := d.Store.ListSessions(store.SessionFilter{Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errors.New("no sessions captured yet; try `credence demo`")
		}
		ref = rows[0].ID
	}
	if strings.HasPrefix(ref, "clm_") {
		row, err := d.Store.GetClaim(ref)
		if err != nil {
			return err
		}
		card := view.Card(d.Store, row)
		if *asJSON {
			entries, _ := d.Store.LedgerEntries(store.LedgerFilter{ClaimID: row.Claim.ID})
			return printJSON(map[string]any{"card": card, "ledger": entries})
		}
		printCard(card, true)
		return nil
	}
	s, err := d.Store.FindSession(ref)
	if err != nil {
		return err
	}
	if *events {
		evs, err := d.Store.Events(s.ID, 0, 0)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(evs)
		}
		for _, e := range evs {
			fmt.Printf("%s %s %-13s %-40s %s\n", dim(fmt.Sprintf("#%-4d", e.Seq)), dim(e.TS.Local().Format("15:04:05")), e.Kind, trunc(e.Actor, 40), trunc(string(e.Payload), 100))
		}
		return nil
	}
	rep, err := view.BuildReport(d.Store, s.ID)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(rep)
	}
	printReport(rep)
	return nil
}

type usageErr string

func (u usageErr) Error() string { return string(u) }

func flag2(s string) error { return usageErr(s) }

func printReport(rep *view.Report) {
	s := rep.Session
	fmt.Printf("%s  %s\n", bold(s.ID), bold(orDash(s.Title)))
	fmt.Printf("%s · %s · %s@%s · %s · %s · $%.2f · %d events\n", s.Harness, orDash(s.Model), orDash(s.RepoName), orDash(s.Branch),
		s.StartedAt.Local().Format("Jan 2 15:04"), dur(s.DurationSec), s.CostUSD, s.EventCount)
	fmt.Println(s.Summary + stateTag(s.SessionRow))
	if len(s.Flags) > 0 {
		fmt.Println(dim("flags: " + strings.Join(s.Flags, ", ")))
	}
	fmt.Println()
	fmt.Println(bold("Timeline"))
	for _, e := range rep.Episodes {
		marker := "  "
		switch e.Kind {
		case "prompt":
			marker = cyan("▸ ")
		case "commit":
			marker = green("● ")
		case "test":
			if e.Failures > 0 {
				marker = red("✗ ")
			} else {
				marker = green("✓ ")
			}
		}
		span := fmt.Sprintf("#%d", e.From)
		if e.To > e.From {
			span = fmt.Sprintf("#%d–%d", e.From, e.To)
		}
		fmt.Printf("  %s %s%s\n", dim(pad(span, 9)), marker, trunc(e.Label, 110))
	}
	for _, sec := range rep.Sections {
		if len(sec.Claims) == 0 {
			continue
		}
		fmt.Printf("\n%s %s\n", sectionColor(sec.Key, bold(strings.ToUpper(sec.Title))), dim("— "+sec.Description))
		for _, c := range sec.Claims {
			printCard(c, false)
		}
	}
	if s.Claims == 0 {
		fmt.Println("\nNo claims for this session.")
	}
}

func statusGlyph(c *view.ClaimCard) string {
	switch c.Status {
	case spec.StatusVerified:
		return green("✓ verified")
	case spec.StatusCorroborated:
		return green("◐ corroborated")
	case spec.StatusContradicted:
		return red("✗ contradicted")
	case spec.StatusOverstated:
		return red("▲ overstated")
	case spec.StatusBlocked:
		return yellow("■ blocked")
	}
	return yellow("○ " + c.Status)
}

func reviewTag(c *view.ClaimCard) string {
	switch c.ReviewState {
	case spec.ReviewAccepted:
		return green(" [accepted]")
	case spec.ReviewEdited:
		return green(" [edited]")
	case spec.ReviewDisputed:
		r := ""
		if c.Review != nil {
			r = ": " + c.Review.Reason
		}
		return red(" [disputed" + r + "]")
	case spec.ReviewRejected:
		return red(" [rejected]")
	}
	return ""
}

func printCard(c *view.ClaimCard, full bool) {
	origin := "agent"
	switch c.Claim.Origin {
	case spec.OriginAuditor:
		origin = "auditor"
	case spec.OriginHuman:
		origin = "human"
	}
	fmt.Printf("\n  %s %s %s%s\n", bold(c.DisplayTitle), dim("("+c.Claim.Type+" · "+origin+" · "+c.Claim.ID+")"), statusGlyph(c), reviewTag(c))
	fmt.Printf("    %s\n", trunc(c.DisplayStatement, 160))
	for _, ch := range c.Checks {
		mark := yellow("·")
		switch ch.Result {
		case spec.ResultPass:
			mark = green("✓")
		case spec.ResultFail:
			mark = red("✗")
		case spec.ResultBlocked:
			mark = yellow("■")
		}
		txt := ch.Plain
		if !full {
			txt = trunc(txt, 150)
		}
		fmt.Printf("    %s %s\n", mark, txt)
		if full && strings.Contains(ch.Details, "\n") {
			for _, l := range strings.Split(ch.Details, "\n")[1:] {
				fmt.Printf("        %s\n", dim(l))
			}
		}
	}
	if len(c.Evidence) > 0 {
		var chips []string
		for _, e := range c.Evidence {
			chips = append(chips, e.Label)
		}
		fmt.Printf("    %s %s\n", dim("evidence:"), strings.Join(chips, dim(" · ")))
	}
	conf := ""
	if c.SelfConfidence != nil {
		conf = fmt.Sprintf("agent confidence %.0f%% → %s · ", *c.SelfConfidence*100, c.Status)
	}
	credit := c.Credit
	if credit != "" {
		credit = "credit " + credit + " · "
	}
	fmt.Printf("    %s\n", dim(fmt.Sprintf("%s%svalue %.2f", conf, credit, c.Value)))
	for _, n := range c.Notes {
		if full || len(c.Checks) == 0 {
			fmt.Printf("    %s %s\n", dim("note:"), trunc(n, 200))
		}
	}
	if len(c.Members) > 0 && full {
		for _, m := range c.Members {
			fmt.Printf("    %s %s (%s, %s)\n", dim("also claimed:"), m.Title, m.Origin, m.Status)
		}
	}
	if c.Review != nil && full {
		fmt.Printf("    %s %s by %s at %s %s\n", dim("review:"), c.Review.Decision, c.Review.By, c.Review.At.Local().Format("Jan 2 15:04"), c.Review.Note)
	}
}

func cmdReport(ctx context.Context, args []string) error {
	fs := newFlags("report", "<session> [--final]")
	final := fs.Bool("final", false, "build the final report (runs the model auditor/judge if configured)")
	rerun := fs.Bool("rerun", false, "re-run tests even when results are cached")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		fs.Usage()
		return flag2("expected a session id")
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	s, err := d.Store.FindSession(pos[0])
	if err != nil {
		return err
	}
	start := time.Now()
	sum, err := d.Pipeline.Report(ctx, s.ID, pipeline.Options{Final: *final || s.State == "closed", Rerun: *rerun, Reverify: true})
	if err != nil {
		return err
	}
	fmt.Printf("%s report for %s: %s (%s)\n", sum.State, s.ID, view.SummaryLine(sum.Claims, sum.Sections), time.Since(start).Round(time.Millisecond))
	if sum.Fallback != "" {
		fmt.Println(yellow("model auditor unavailable, used rules: ") + sum.Fallback)
	}
	if sum.Skipped != "" {
		fmt.Println(dim("skipped: " + sum.Skipped))
	}
	return nil
}

func cmdVerify(ctx context.Context, args []string) error {
	fs := newFlags("verify", "<claim> [--rerun]")
	rerun := fs.Bool("rerun", false, "re-run tests even when results are cached")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		fs.Usage()
		return flag2("expected a claim id")
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	row, err := d.Store.GetClaim(pos[0])
	if err != nil {
		return err
	}
	fmt.Printf("Verifying %s %q…\n", row.Claim.ID, row.Claim.Title)
	_, dec, err := d.Pipeline.VerifyOne(ctx, row.Claim.ID, *rerun)
	if err != nil {
		return err
	}
	row, _ = d.Store.GetClaim(row.Claim.ID)
	printCard(view.Card(d.Store, row), true)
	if dec != nil && len(dec.Missing) > 0 {
		fmt.Println(dim("\nmissing: " + strings.Join(dec.Missing, ", ")))
	}
	return nil
}

var errNoTTY = errors.New("interactive review needs a terminal; use `credence review <claim> --accept` etc.")
