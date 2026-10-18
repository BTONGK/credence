package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/calibration"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/rollup"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

func cmdLedger(ctx context.Context, args []string) error {
	fs := newFlags("ledger", "[--by agent|repo|harness|model] [--entries] [--verify [file]] [--json]")
	by := fs.String("by", "agent", "group reputation by agent, repo, harness or model")
	entries := fs.Bool("entries", false, "list ledger entries")
	verify := fs.Bool("verify", false, "verify the hash chain and signatures (of the local ledger, or of an exported file given as argument)")
	asJSON := fs.Bool("json", false, "JSON output")
	limit := fs.Int("limit", 50, "entries to list")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *verify && len(pos) == 1 {
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer f.Close()
		ex, err := ledger.ReadExport(f)
		if err != nil {
			return err
		}
		pub, err := keys.ParsePublicKey(ex.PublicKey)
		if err != nil {
			return fmt.Errorf("export public key: %w", err)
		}
		rep := ledger.VerifyChain(ex.Entries, pub)
		return printVerify(rep, *asJSON)
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	if *verify {
		es, err := d.Store.LedgerEntries(store.LedgerFilter{})
		if err != nil {
			return err
		}
		return printVerify(ledger.VerifyChain(es, d.Signer.Pub), *asJSON)
	}
	if *entries {
		es, err := d.Store.LedgerEntries(store.LedgerFilter{})
		if err != nil {
			return err
		}
		if len(es) > *limit {
			es = es[len(es)-*limit:]
		}
		if *asJSON {
			return printJSON(es)
		}
		for _, e := range es {
			var c spec.Claim
			_ = jsonUnmarshal(e.Claim, &c)
			what := e.Kind
			if e.HumanDecision != nil {
				what = e.HumanDecision.Decision + " by " + e.HumanDecision.By
				if e.HumanDecision.Reason != "" {
					what += " (" + e.HumanDecision.Reason + ")"
				}
			}
			if e.Survival != nil {
				what = fmt.Sprintf("survival %s: %s (%.0f%%)", e.Survival.Window, e.Survival.Result, e.Survival.Ratio*100)
			}
			fmt.Printf("%s %s %-14s %-12s %s · %s\n", dim(fmt.Sprintf("#%-4d", e.Seq)), dim(e.CreatedAt.Local().Format("Jan 2 15:04")), c.ID, e.Status, trunc(c.Title, 60), what)
		}
		if len(es) == 0 {
			fmt.Println("The ledger is empty. Decisions you make in review are recorded here.")
		}
		return nil
	}
	stats, err := calibration.Compute(d.Store, calibration.GroupBy(*by), time.Time{})
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(stats)
	}
	if len(stats) == 0 {
		fmt.Println("No claims yet.")
		return nil
	}
	cfgs, _ := d.Store.AgentConfigs()
	names := map[string]string{}
	for _, c := range cfgs {
		names[c.ID] = c.Harness + "/" + c.Model
	}
	fmt.Printf("%s\n\n", bold("Reputation by "+*by))
	fmt.Printf("%-28s %5s %6s %9s %8s %8s %8s %12s %8s %9s\n", "", "sess", "claims", "verified", "contra", "accepted", "disputed", "brier (n)", "value", "value/$")
	for _, s := range stats {
		key := s.Key
		if n := names[key]; n != "" {
			key = calibration.ConfigOf(key) + " " + dim(n)
		}
		brier := "-"
		if s.BrierN > 0 {
			brier = fmt.Sprintf("%.3f (%d)", s.Brier, s.BrierN)
		}
		vpd := "-"
		if s.CostUSD > 0 {
			vpd = fmt.Sprintf("%.1f", s.ValuePerDollar)
		}
		fmt.Printf("%s %5d %6d %9s %8s %8d %8d %12s %8.1f %9s\n", pad(trunc(key, 28), 28), s.Sessions, s.Claims, pct(s.Verified+s.Corroborated, s.Claims),
			pct(s.Contradicted, s.Claims), s.Accepted+s.Edited, s.Disputed+s.Rejected, brier, s.Value, vpd)
		if s.BrierN > 0 && s.Overconfidence > 0.1 {
			fmt.Printf("%s\n", yellow(fmt.Sprintf("    over-confident: stated confidence exceeds outcomes by %.0f points", s.Overconfidence*100)))
		}
		if s.Surviving+s.Decayed+s.Reverted > 0 {
			fmt.Printf("%s\n", dim(fmt.Sprintf("    survival: %d surviving · %d decayed · %d reverted", s.Surviving, s.Decayed, s.Reverted)))
		}
	}
	return nil
}

func pct(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", a*100/b)
}

func printVerify(rep ledger.Report, asJSON bool) error {
	if asJSON {
		return printJSON(rep)
	}
	if rep.OK {
		if rep.Entries == 0 {
			fmt.Printf("%s no entries yet (entries are written when you review claims), key %s\n", green("✓ ledger verifies:"), rep.KeyID)
			return nil
		}
		fmt.Printf("%s %d entries, chain head %s, key %s\n", green("✓ ledger verifies:"), rep.Entries, short(rep.Head), rep.KeyID)
		return nil
	}
	fmt.Printf("%s %d problems in %d entries\n", red("✗ ledger does not verify:"), len(rep.Problems), rep.Entries)
	for _, p := range rep.Problems {
		fmt.Printf("  #%d %s\n", p.Seq, p.Reason)
	}
	return fmt.Errorf("ledger verification failed")
}

func short(s string) string {
	s = strings.TrimPrefix(s, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func cmdExport(ctx context.Context, args []string) error {
	fs := newFlags("export", "--format json|csv [--out file] [--share-person]")
	format := fs.String("format", "json", "json or csv")
	out := fs.String("out", "", "write to a file instead of stdout")
	share := fs.Bool("share-person", config.Load().PersonViews, "sign an opt-in to appear by name in team rollups (json only)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	es, err := d.Store.LedgerEntries(store.LedgerFilter{})
	if err != nil {
		return err
	}
	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	switch *format {
	case "json":
		ex := rollup.TeamExport{Export: ledger.NewExport(es, d.Signer.Pub)}
		if *share {
			person := reviewer(es)
			if person == "" {
				return errors.New("--share-person: no reviews in the ledger yet")
			}
			if ex.OptIn, err = rollup.SignOptIn(d.Signer, person, ex.Head); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "signed an opt-in for %s to appear by name in team rollups\n", person)
		}
		err = ledger.Encode(w, ex)
	case "csv":
		err = ledger.WriteCSV(w, es, func(id string) float64 {
			if c, err := d.Store.GetClaim(id); err == nil {
				return c.Value
			}
			return 0
		})
	default:
		return fmt.Errorf("unknown format %q", *format)
	}
	if err == nil && *out != "" {
		fmt.Fprintf(os.Stderr, "wrote %d entries to %s\n", len(es), *out)
	}
	return err
}

// cmdContributors aggregates accepted claims into credits.
func cmdContributors(ctx context.Context, args []string) error {
	fs := newFlags("contributors", "[--since 30d] [--format md|json] [--release name] [--repo r]")
	since := fs.String("since", "", "only claims decided within this window (e.g. 30d)")
	format := fs.String("format", "md", "md or json")
	release := fs.String("release", "", "render release notes with this heading")
	repo := fs.String("repo", "", "only this repo")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	var from time.Time
	if *since != "" {
		dd, err := policy.ParseWindow(*since)
		if err != nil {
			return err
		}
		from = time.Now().Add(-dd)
	}
	rows, err := d.Store.ListClaims(store.ClaimFilter{ActiveOnly: true, PrimaryOnly: true})
	if err != nil {
		return err
	}
	type credit struct {
		ID     string   `json:"contributor"`
		Value  float64  `json:"value"`
		Claims int      `json:"claims"`
		Titles []string `json:"titles"`
	}
	credits := map[string]*credit{}
	byType := map[string][]string{}
	for _, r := range rows {
		if r.ReviewState != spec.ReviewAccepted && r.ReviewState != spec.ReviewEdited {
			continue
		}
		if r.Review != nil && !from.IsZero() && r.Review.At.Before(from) {
			continue
		}
		if r.SurvivalState == spec.SurvivalReverted {
			continue
		}
		if *repo != "" {
			if s, err := d.Store.GetSession(r.Claim.SessionID); err != nil || (s.RepoName != *repo && s.Repo != *repo) {
				continue
			}
		}
		title := r.Claim.Title
		if r.Review != nil && r.Review.EditedTitle != "" {
			title = r.Review.EditedTitle
		}
		a, _ := d.Store.GetAttribution(r.Claim.ID)
		if a == nil {
			continue
		}
		var who []string
		for _, s := range a.Shares {
			id := contributorKey(s.Contributor)
			c := credits[id]
			if c == nil {
				c = &credit{ID: id}
				credits[id] = c
			}
			c.Value += r.Value * s.Share
			c.Claims++
			c.Titles = append(c.Titles, title)
			who = append(who, view.ShortContributor(s.Contributor))
		}
		byType[r.Claim.Type] = append(byType[r.Claim.Type], fmt.Sprintf("%s (%s)", title, strings.Join(uniqS(who), ", ")))
	}
	var list []*credit
	for _, c := range credits {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Value > list[j].Value })
	if *format == "json" {
		return printJSON(map[string]any{"contributors": list, "by_type": byType})
	}
	if *release != "" {
		fmt.Printf("## %s\n\n", *release)
		for _, t := range spec.ClaimTypes {
			if len(byType[t]) == 0 {
				continue
			}
			fmt.Printf("### %s\n\n", releaseHeading(t))
			for _, l := range byType[t] {
				fmt.Printf("- %s\n", l)
			}
			fmt.Println()
		}
	}
	fmt.Println("## Contributors")
	fmt.Println()
	fmt.Println("Credit from human-reviewed, evidence-verified claims (generated by Credence).")
	fmt.Println()
	for _, c := range list {
		fmt.Printf("- **%s**: %.1f credit across %d claims\n", c.ID, c.Value, c.Claims)
	}
	if len(list) == 0 {
		fmt.Println("_No accepted claims yet._")
	}
	return nil
}

func contributorKey(id string) string {
	switch {
	case strings.HasPrefix(id, "human:"):
		return strings.TrimPrefix(id, "human:")
	case strings.HasPrefix(id, "agent:"):
		s := strings.TrimPrefix(id, "agent:")
		if i := strings.Index(s, "@"); i > 0 {
			s = s[:i]
		}
		return s + " (agent)"
	case strings.HasPrefix(id, "subagent:"):
		return "subagents"
	}
	return id
}

func releaseHeading(t string) string {
	switch t {
	case spec.TypeFix:
		return "Fixes"
	case spec.TypeFeature:
		return "Features"
	case spec.TypeTest:
		return "Tests"
	case spec.TypeDocs:
		return "Documentation"
	case spec.TypeRefactor:
		return "Refactoring"
	case spec.TypeOps:
		return "Build and CI"
	case spec.TypeNegativeResult:
		return "Investigated and ruled out"
	}
	return strings.ToUpper(t[:1]) + strings.ReplaceAll(t[1:], "_", " ")
}

func uniqS(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// reviewer is the person who made most of the ledger's decisions.
func reviewer(es []spec.LedgerEntry) string {
	n := map[string]int{}
	best := ""
	for _, e := range es {
		if e.HumanDecision == nil || e.HumanDecision.By == "" {
			continue
		}
		by := e.HumanDecision.By
		n[by]++
		if n[by] > n[best] || (n[by] == n[best] && by < best) {
			best = by
		}
	}
	return best
}
