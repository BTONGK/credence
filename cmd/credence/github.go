package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/verifiers"
)

const prMarker = "<!-- credence:pr-comment -->"

type prClaim struct {
	ID, Title, Type, Status, Review, By, Credit string
	Commits                                     []string
	KeyID                                       string
	Valid                                       bool
	Signed                                      bool
	Untrusted                                   bool // signed by a key not in --trusted-keys
}

// cmdPRComment summarizes claims for the commits of a pull request. In CI
// it reads signed entries from refs/notes/credence; locally it can also use
// the local ledger.
func cmdPRComment(ctx context.Context, args []string) error {
	fs := newFlags("pr-comment", "[--pr n] [--repo owner/name] [--base ref] [--head ref] [--source notes|local] [--trusted-keys ids] [--dry-run]")
	prNum := fs.Int("pr", 0, "pull request number (default from GITHUB_EVENT_PATH)")
	repoFlag := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/name")
	base := fs.String("base", "", "base ref or sha (default from the PR event, else origin/main)")
	head := fs.String("head", "HEAD", "head ref or sha")
	source := fs.String("source", "notes", "notes (signed git notes) or local (this machine's ledger)")
	trusted := fs.String("trusted-keys", os.Getenv("CREDENCE_TRUSTED_KEYS"), "comma-separated signing key ids (from `credence ledger verify`) whose reviews count")
	dry := fs.Bool("dry-run", false, "print the comment instead of posting it")
	dir := fs.String("C", ".", "repository directory")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	repo := gitx.Root(*dir)
	if repo == "" {
		return errors.New("not in a git repository")
	}
	if ev := os.Getenv("GITHUB_EVENT_PATH"); ev != "" {
		var e struct {
			Number      int `json:"number"`
			PullRequest struct {
				Number int `json:"number"`
				Base   struct {
					SHA string `json:"sha"`
				} `json:"base"`
				Head struct {
					SHA string `json:"sha"`
				} `json:"head"`
			} `json:"pull_request"`
		}
		if b, err := os.ReadFile(ev); err == nil && json.Unmarshal(b, &e) == nil {
			if *prNum == 0 {
				*prNum = e.PullRequest.Number
			}
			if *base == "" {
				*base = e.PullRequest.Base.SHA
			}
			if *head == "HEAD" && e.PullRequest.Head.SHA != "" {
				*head = e.PullRequest.Head.SHA
			}
		}
	}
	if *base == "" {
		*base = "origin/" + gitx.DefaultBranch(repo)
	}
	shas, err := gitx.CommitsInRange(repo, *base, *head)
	if err != nil {
		return fmt.Errorf("commits %s..%s: %w", *base, *head, err)
	}
	inPR := map[string]bool{}
	for _, s := range shas {
		inPR[s] = true
	}
	var items []prClaim
	if *source == "local" {
		items, err = localPRClaims(inPR)
		if err != nil {
			return err
		}
	} else {
		items = notePRClaims(repo, shas, trustedKeys(*trusted))
	}
	body := renderPRComment(items, len(shas), *trusted != "")
	if *dry || *prNum == 0 || *repoFlag == "" {
		fmt.Println(body)
		if !*dry && *prNum == 0 {
			fmt.Fprintln(os.Stderr, dim("(no PR number; printed instead of posting)"))
		}
		return nil
	}
	return upsertComment(ctx, *repoFlag, *prNum, body)
}

// trustedKeys parses key ids or full "ed25519:<base64>" public keys.
func trustedKeys(s string) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if pub, err := keys.ParsePublicKey(k); err == nil {
			k = keys.KeyID(pub)
		}
		out[k] = true
	}
	return out
}

func notePRClaims(repo string, shas []string, trusted map[string]bool) []prClaim {
	latest := map[string]prClaim{}
	order := map[string]string{}
	for _, sha := range shas {
		for _, n := range ledger.ParseNotes(gitx.ReadNote(repo, sha)) {
			e := n.Entry
			var c spec.Claim
			_ = json.Unmarshal(e.Claim, &c)
			pc := prClaim{ID: e.ClaimID, Title: c.Title, Type: c.Type, Status: e.Status, Commits: e.Commits, KeyID: n.KeyID, Valid: n.Valid, Signed: true,
				Untrusted: len(trusted) > 0 && !trusted[n.KeyID]}
			if e.HumanDecision != nil {
				pc.Review, pc.By = e.HumanDecision.Decision, e.HumanDecision.By
				if e.HumanDecision.EditedTitle != "" {
					pc.Title = e.HumanDecision.EditedTitle
				}
			}
			if e.Attribution != nil {
				pc.Credit = view.CreditLine(e.Attribution)
			}
			if e.Survival != nil {
				if prev, ok := latest[e.ClaimID]; ok {
					prev.Status += " · " + e.Survival.Result
					latest[e.ClaimID] = prev
				}
				continue
			}
			ts := e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
			if order[e.ClaimID] <= ts {
				order[e.ClaimID] = ts
				latest[e.ClaimID] = pc
			}
		}
	}
	var out []prClaim
	for _, v := range latest {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].ID] < order[out[j].ID] })
	return out
}

func localPRClaims(inPR map[string]bool) ([]prClaim, error) {
	d, err := open()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, err := d.Store.ListClaims(store.ClaimFilter{ActiveOnly: true, PrimaryOnly: true})
	if err != nil {
		return nil, err
	}
	var out []prClaim
	for _, r := range rows {
		var commits []string
		for _, ev := range r.Claim.Evidence {
			c := spec.ParseTarget(ev.Target).Commit
			for full := range inPR {
				if c != "" && strings.HasPrefix(full, c) {
					commits = append(commits, full)
				}
			}
		}
		if len(commits) == 0 {
			continue
		}
		card := view.Card(d.Store, r)
		pc := prClaim{ID: r.Claim.ID, Title: card.DisplayTitle, Type: r.Claim.Type, Status: r.Status, Credit: card.Credit, Commits: commits, Review: r.ReviewState}
		if r.Review != nil {
			pc.By = r.Review.By
		}
		out = append(out, pc)
	}
	return out, nil
}

func renderPRComment(items []prClaim, commits int, pinned bool) string {
	var b strings.Builder
	b.WriteString(prMarker + "\n### Credence: agent work in this PR\n\n")
	if len(items) == 0 {
		fmt.Fprintf(&b, "No signed Credence claims for the %d commits in this PR. Reviewed claims appear here when contributors enable `ledger.git_notes` and push `refs/notes/credence`.\n", commits)
		return b.String()
	}
	b.WriteString("| | Claim | Type | Status | Review | Credit |\n|---|---|---|---|---|---|\n")
	keyIDs := map[string]bool{}
	invalid, untrusted := 0, 0
	for _, it := range items {
		mark := "○"
		switch {
		case strings.HasPrefix(it.Status, spec.StatusVerified):
			mark = "✅"
		case strings.HasPrefix(it.Status, spec.StatusCorroborated):
			mark = "☑️"
		case strings.HasPrefix(it.Status, spec.StatusContradicted), strings.HasPrefix(it.Status, spec.StatusOverstated):
			mark = "❌"
		}
		review := it.Review
		if it.By != "" {
			review += " by " + strings.TrimPrefix(it.By, "human:")
		}
		if it.Signed && !it.Valid {
			mark = "⚠️"
			invalid++
		} else if it.Untrusted {
			mark = "⚠️"
			review += " (untrusted key `" + it.KeyID + "`)"
			untrusted++
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", mark, mdEscape(it.Title), it.Type, it.Status, mdEscape(review), mdEscape(it.Credit))
		if it.KeyID != "" {
			keyIDs[it.KeyID] = true
		}
	}
	if len(keyIDs) > 0 {
		var ks []string
		for k := range keyIDs {
			ks = append(ks, "`"+k+"`")
		}
		sort.Strings(ks)
		switch {
		case invalid > 0:
			fmt.Fprintf(&b, "\n⚠️ %s failed signature or hash verification.\n", countEntries(invalid))
		case untrusted > 0:
			verb := "are"
			if untrusted == 1 {
				verb = "is"
			}
			fmt.Fprintf(&b, "\n⚠️ %s %s signed by a key outside this repository's `trusted-keys`.\n", countEntries(untrusted), verb)
		case pinned:
			fmt.Fprintf(&b, "\nEvery entry is signed by a trusted key (%s).\n", strings.Join(ks, ", "))
		default:
			fmt.Fprintf(&b, "\nEvery entry carries a valid signature (keys %s). A signature shows an entry is unaltered, not who signed it; set `trusted-keys` to pin reviewers' keys.\n", strings.Join(ks, ", "))
		}
	}
	b.WriteString("\n<sub>Generated by [Credence](https://github.com/BTONGK/credence): agents propose credit; evidence decides.</sub>\n")
	return b.String()
}

func countEntries(n int) string {
	if n == 1 {
		return "1 entry"
	}
	return fmt.Sprintf("%d entries", n)
}

func mdEscape(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s) }

func upsertComment(ctx context.Context, repo string, pr int, body string) error {
	gh := verifiers.NewGitHub()
	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := gh.Get(ctx, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", repo, pr), &comments); err != nil {
		return err
	}
	for _, c := range comments {
		if strings.Contains(c.Body, prMarker) {
			if err := gh.Do(ctx, "PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", repo, c.ID), map[string]string{"body": body}, nil); err != nil {
				return err
			}
			fmt.Printf("updated Credence comment on %s#%d\n", repo, pr)
			return nil
		}
	}
	if err := gh.Do(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, pr), map[string]string{"body": body}, nil); err != nil {
		return err
	}
	fmt.Printf("posted Credence comment on %s#%d\n", repo, pr)
	return nil
}
