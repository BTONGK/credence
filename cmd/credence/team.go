package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/rollup"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/policy"
)

// cmdTeam aggregates teammates' signed ledgers. Each person shares
// `credence export --format json` (with --share-person to be named); the
// rollup verifies every source and never sees a transcript.
func cmdTeam(ctx context.Context, args []string) error {
	fs := newFlags("team", "rollup <export.json|dir>… [--notes repo] [--by agent|repo|harness|model|person] [--since 30d] [--trusted-keys ids] [--format text|md|json]")
	by := fs.String("by", "agent", "group by agent, repo, harness, model or person (opted-in people only)")
	since := fs.String("since", "", "only decisions in this window, e.g. 30d")
	notes := fs.String("notes", "", "also read signed entries from this repository's refs/notes/credence")
	trusted := fs.String("trusted-keys", os.Getenv("CREDENCE_TRUSTED_KEYS"), "comma-separated key ids or ed25519: public keys to accept (default: any valid source)")
	format := fs.String("format", "text", "text, md or json")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 || pos[0] != "rollup" {
		return errors.New("usage: credence team rollup <export.json|dir>… [--notes repo] [--by agent|repo|harness|model|person]")
	}
	switch *by {
	case "agent", "repo", "harness", "model", "person":
	default:
		return fmt.Errorf("--by must be agent, repo, harness, model or person")
	}
	srcs, err := rollup.ReadPaths(pos[1:])
	if err != nil {
		return err
	}
	if *notes != "" {
		repo := gitx.Root(*notes)
		if repo == "" {
			return fmt.Errorf("%s is not a git repository", *notes)
		}
		ns, err := rollup.ReadNotes(repo)
		if err != nil {
			return err
		}
		srcs = append(srcs, ns...)
	}
	if len(srcs) == 0 {
		return errors.New("no ledgers given: pass exported JSON files or directories, or --notes <repo>")
	}
	o := rollup.Options{By: *by, Trusted: trustedKeys(*trusted)}
	if *trusted == "" {
		o.Trusted = nil
	}
	if *since != "" {
		d, err := policy.ParseWindow(*since)
		if err != nil {
			return err
		}
		o.Since = time.Now().Add(-d)
	}
	r := rollup.Compute(srcs, o)
	if *format == "json" {
		return ledger.Encode(os.Stdout, r)
	}
	md := *format == "md"
	if md {
		fmt.Printf("### Agent work across %s\n\n", plural(len(srcs), "ledger"))
	} else {
		fmt.Println(bold("Sources"))
	}
	for _, s := range r.Sources {
		state := green("verified")
		switch {
		case !s.OK:
			state = red("does not verify, excluded")
		case !s.Trusted:
			state = yellow("untrusted key, excluded")
		}
		who := ""
		if s.Person != "" {
			who = " · shares as " + s.Person
		}
		line := fmt.Sprintf("%s  key %s  %s  %s%s", s.Name, s.KeyID, plural(s.Entries, "entry"), state, who)
		if md {
			line = "- " + stripANSI(line)
		} else {
			line = "  " + line
		}
		fmt.Println(line)
		for _, p := range firstStrings(s.Problems, 3) {
			fmt.Println("    " + dim(p))
		}
	}
	fmt.Println()
	rollup.WriteText(os.Stdout, r, md)
	if !md {
		fmt.Println()
		fmt.Println(dim("Value weighs accepted claims by type (edited counts half) and drops reverted work. Calibration is the Brier score of agents' stated confidence; lower is better."))
		if *by == "person" {
			fmt.Println(dim("Only people who exported with --share-person appear by name; rows are alphabetical."))
		}
	}
	return nil
}

func firstStrings(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], fmt.Sprintf("… %d more", len(xs)-n))
	}
	return xs
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if word == "entry" {
		return fmt.Sprintf("%d entries", n)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
