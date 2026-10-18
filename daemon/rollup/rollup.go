// Package rollup aggregates signed ledgers from several people into a team
// view. It reads ledger exports and git notes, never transcripts, verifies
// every entry, and groups by agent configuration, repo, harness or model.
// Per-person rows exist only for people who signed an opt-in into their
// own export; everyone else is counted in one "not shared" row.
package rollup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// OptInSpec prefixes the signed message of a person-level opt-in.
const OptInSpec = "credence.person-opt-in/0.9"

// OptIn is a person's signed consent to appear by name in team rollups.
type OptIn struct {
	Person    string `json:"person"`
	Head      string `json:"head"`
	Signature string `json:"signature"`
}

func optInMessage(person, head string) string { return OptInSpec + "\n" + person + "\n" + head }

// SignOptIn signs an opt-in for person over the export head.
func SignOptIn(s *keys.Signer, person, head string) (*OptIn, error) {
	sig, err := s.Sign(optInMessage(person, head))
	if err != nil {
		return nil, err
	}
	return &OptIn{Person: person, Head: head, Signature: sig}, nil
}

// TeamExport is a ledger export with an optional opt-in.
type TeamExport struct {
	ledger.Export
	OptIn *OptIn `json:"person_opt_in,omitempty"`
}

// Source is one input after verification.
type Source struct {
	Name     string   `json:"name"`
	KeyID    string   `json:"key_id"`
	Entries  int      `json:"entries"`
	OK       bool     `json:"ok"`
	Trusted  bool     `json:"trusted"`
	Person   string   `json:"person,omitempty"` // set when a valid opt-in covers it
	Problems []string `json:"problems,omitempty"`

	entries []spec.LedgerEntry
}

// Options select and group entries.
type Options struct {
	By      string          // agent | repo | harness | model | person
	Since   time.Time       // decisions before this are ignored
	Trusted map[string]bool // key ids; empty trusts every valid source
	Policy  *policy.Policy  // claim type weights; default policy if nil
}

// Row is one group.
type Row struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	Claims       int     `json:"claims"`
	Accepted     int     `json:"accepted"`
	Edited       int     `json:"edited"`
	Disputed     int     `json:"disputed"`
	Rejected     int     `json:"rejected"`
	Verified     int     `json:"verified"`
	Contradicted int     `json:"contradicted"`
	Survived30   int     `json:"survived_30d"`
	Checked30    int     `json:"checked_30d"`
	Reverted     int     `json:"reverted"`
	Value        float64 `json:"value"`
	Brier        float64 `json:"brier"`
	BrierN       int     `json:"brier_n"`
}

// Rollup is the team view.
type Rollup struct {
	By      string    `json:"by"`
	Since   time.Time `json:"since,omitempty"`
	Sources []*Source `json:"sources"`
	Rows    []*Row    `json:"rows"`
	Claims  int       `json:"claims"`
	Dropped int       `json:"dropped_entries"` // entries from invalid or untrusted sources
}

// ReadExport reads one JSON export file.
func ReadExport(path string) (*Source, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ex TeamExport
	if err := json.Unmarshal(b, &ex); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	src := &Source{Name: filepath.Base(path), KeyID: ex.KeyID, Entries: len(ex.Entries), entries: ex.Entries}
	pub, err := keys.ParsePublicKey(ex.PublicKey)
	if err != nil {
		src.Problems = append(src.Problems, "no valid public key")
		return src, nil
	}
	src.KeyID = keys.KeyID(pub)
	rep := ledger.VerifyChain(ex.Entries, pub)
	src.OK = rep.OK
	for _, p := range rep.Problems {
		src.Problems = append(src.Problems, fmt.Sprintf("#%d %s", p.Seq, p.Reason))
	}
	if ex.OptIn != nil {
		if ex.OptIn.Head == rep.Head && keys.Verify(pub, optInMessage(ex.OptIn.Person, ex.OptIn.Head), ex.OptIn.Signature) {
			src.Person = ex.OptIn.Person
		} else {
			src.Problems = append(src.Problems, "person opt-in does not verify; counted as not shared")
		}
	}
	return src, nil
}

// ReadPaths reads export files, and every *.json file in directories.
func ReadPaths(paths []string) ([]*Source, error) {
	var out []*Source
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		files := []string{p}
		if fi.IsDir() {
			files, _ = filepath.Glob(filepath.Join(p, "*.json"))
			sort.Strings(files)
		}
		for _, f := range files {
			s, err := ReadExport(f)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// ReadNotes reads signed entries from refs/notes/credence in repo, one
// source per signing key. Notes carry no opt-in, so nobody is named.
func ReadNotes(repo string) ([]*Source, error) {
	out, err := gitx.Run(repo, "notes", "--ref="+gitx.NotesRef, "list")
	if err != nil {
		return nil, fmt.Errorf("%s has no %s: %w", repo, gitx.NotesRef, err)
	}
	byKey := map[string]*Source{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		for _, n := range ledger.ParseNotes(gitx.ReadNote(repo, f[1])) {
			s := byKey[n.KeyID]
			if s == nil {
				s = &Source{Name: "notes:" + filepath.Base(repo), KeyID: n.KeyID, OK: true}
				byKey[n.KeyID] = s
				order = append(order, n.KeyID)
			}
			s.Entries++
			if !n.Valid {
				// Each note verifies on its own; drop the bad ones only.
				s.Problems = append(s.Problems, fmt.Sprintf("entry %s on %s does not verify", spec.ShortHex(n.Entry.EntryHash, 8), f[1][:7]))
				continue
			}
			s.entries = append(s.entries, *n.Entry)
		}
	}
	var srcs []*Source
	for _, k := range order {
		srcs = append(srcs, byKey[k])
	}
	return srcs, nil
}

type claimState struct {
	decision *spec.LedgerEntry
	survival map[string]string // window -> result
	person   string            // reviewer, if they opted in
}

// Compute aggregates verified, trusted sources.
func Compute(srcs []*Source, o Options) *Rollup {
	pol := o.Policy
	if pol == nil {
		pol = policy.Default()
	}
	by := o.By
	if by == "" {
		by = "agent"
	}
	r := &Rollup{By: by, Since: o.Since, Sources: srcs}
	seen := map[string]bool{}
	claimsByID := map[string]*claimState{}
	// Opted-in exports first, so an entry also mirrored to git notes keeps
	// its reviewer's opt-in.
	ordered := append([]*Source(nil), srcs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Person != "" && ordered[j].Person == "" })
	for _, s := range ordered {
		s.Trusted = len(o.Trusted) == 0 || o.Trusted[s.KeyID]
		// An export that fails verification is dropped whole; notes are
		// verified one by one, so their good entries remain.
		if !s.OK || !s.Trusted {
			r.Dropped += s.Entries
			continue
		}
		for i := range s.entries {
			e := &s.entries[i]
			if seen[e.EntryHash] {
				continue
			}
			seen[e.EntryHash] = true
			cs := claimsByID[e.ClaimID]
			if cs == nil {
				cs = &claimState{survival: map[string]string{}}
				claimsByID[e.ClaimID] = cs
			}
			switch {
			case e.Survival != nil:
				cs.survival[e.Survival.Window] = e.Survival.Result
			case e.HumanDecision != nil:
				if cs.decision == nil || !e.CreatedAt.Before(cs.decision.CreatedAt) {
					cs.decision = e
					cs.person = ""
					if s.Person != "" && e.HumanDecision.By == s.Person {
						cs.person = s.Person
					}
				}
			}
		}
	}
	rows := map[string]*Row{}
	for _, cs := range claimsByID {
		e := cs.decision
		if e == nil || (!o.Since.IsZero() && e.CreatedAt.Before(o.Since)) {
			continue
		}
		var c spec.Claim
		_ = json.Unmarshal(e.Claim, &c)
		key, label := groupKey(by, e, &c, cs.person)
		row := rows[key]
		if row == nil {
			row = &Row{Key: key, Label: label}
			rows[key] = row
		}
		r.Claims++
		row.Claims++
		factor := 0.0
		y := -1.0
		switch e.HumanDecision.Decision {
		case spec.ReviewAccepted:
			row.Accepted++
			factor, y = 1, 1
		case spec.ReviewEdited:
			row.Edited++
			factor, y = 0.5, 0.5
		case spec.ReviewDisputed:
			row.Disputed++
			y = 0
		case spec.ReviewRejected:
			row.Rejected++
			y = 0
		}
		switch e.Status {
		case spec.StatusVerified, spec.StatusCorroborated:
			row.Verified++
		case spec.StatusContradicted, spec.StatusOverstated:
			row.Contradicted++
		}
		if res, ok := cs.survival["30d"]; ok {
			row.Checked30++
			if res == spec.SurvivalSurviving {
				row.Survived30++
			}
		}
		for _, res := range cs.survival {
			if res == spec.SurvivalReverted {
				row.Reverted++
				factor = 0
				break
			}
		}
		row.Value += pol.Type(c.Type).Weight * factor
		if c.Origin == spec.OriginSelf && c.SelfConfidence != nil && y >= 0 {
			p := *c.SelfConfidence
			row.Brier += (p - y) * (p - y)
			row.BrierN++
		}
	}
	for _, row := range rows {
		if row.BrierN > 0 {
			row.Brier /= float64(row.BrierN)
		}
		r.Rows = append(r.Rows, row)
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if by == "person" {
			// Alphabetical, with the unshared row last: not a leaderboard.
			if (a.Key == notShared) != (b.Key == notShared) {
				return b.Key == notShared
			}
			return a.Label < b.Label
		}
		if a.Value != b.Value {
			return a.Value > b.Value
		}
		return a.Key < b.Key
	})
	return r
}

const notShared = "~not-shared"

func groupKey(by string, e *spec.LedgerEntry, c *spec.Claim, person string) (string, string) {
	harness, model, cfg := parseClaimant(c.Claimant)
	if cfg == "" {
		cfg = e.AgentConfig
	}
	switch by {
	case "repo":
		return orUnknown(e.Repo), orUnknown(e.Repo)
	case "harness":
		return orUnknown(harness), orUnknown(harness)
	case "model":
		return orUnknown(model), orUnknown(model)
	case "person":
		if person == "" {
			return notShared, "people who have not opted in"
		}
		return person, strings.TrimPrefix(person, "human:")
	}
	label := strings.Trim(harness+"/"+model, "/")
	if label == "" {
		label = "unknown agent"
	}
	if cfg == "" {
		return "unknown", label
	}
	return cfg, label
}

// parseClaimant splits agent:<harness>/<model>@<cfg>.
func parseClaimant(id string) (harness, model, cfg string) {
	if !strings.HasPrefix(id, "agent:") {
		return "", "", ""
	}
	id = strings.TrimPrefix(id, "agent:")
	if i := strings.LastIndex(id, "@"); i >= 0 {
		id, cfg = id[:i], id[i+1:]
	}
	harness, model, _ = strings.Cut(id, "/")
	return harness, model, cfg
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// WriteText renders the rollup for a terminal or, with md, as Markdown.
func WriteText(w io.Writer, r *Rollup, md bool) {
	head := map[string]string{"agent": "Agent config", "repo": "Repo", "harness": "Harness", "model": "Model", "person": "Person"}[r.By]
	cols := []string{head, "Claims", "Accepted", "Disputed", "Verified", "Contradicted", "Survived 30d", "Value", "Calibration"}
	var lines [][]string
	for _, x := range r.Rows {
		label := x.Label
		if r.By == "agent" && x.Key != "unknown" {
			label = x.Key + " (" + x.Label + ")"
		}
		surv := "—"
		if x.Checked30 > 0 {
			surv = fmt.Sprintf("%d/%d", x.Survived30, x.Checked30)
		}
		cal := "—"
		if x.BrierN > 0 {
			cal = fmt.Sprintf("%.3f (%d)", x.Brier, x.BrierN)
		}
		lines = append(lines, []string{label, fmt.Sprint(x.Claims), fmt.Sprint(x.Accepted + x.Edited), fmt.Sprint(x.Disputed + x.Rejected),
			pctOf(x.Verified, x.Claims), pctOf(x.Contradicted, x.Claims), surv, fmt.Sprintf("%.1f", x.Value), cal})
	}
	if md {
		fmt.Fprintf(w, "| %s |\n|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
		for _, l := range lines {
			fmt.Fprintf(w, "| %s |\n", strings.Join(l, " | "))
		}
	} else {
		width := make([]int, len(cols))
		for i, c := range cols {
			width[i] = len([]rune(c))
		}
		for _, l := range lines {
			for i, c := range l {
				if n := len([]rune(c)); n > width[i] {
					width[i] = n
				}
			}
		}
		row := func(l []string) {
			var b strings.Builder
			for i, c := range l {
				pad := width[i] - len([]rune(c))
				if i == 0 {
					b.WriteString(c + strings.Repeat(" ", pad))
				} else {
					b.WriteString("  " + strings.Repeat(" ", pad) + c)
				}
			}
			fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
		}
		row(cols)
		for _, l := range lines {
			row(l)
		}
	}
	if len(r.Rows) == 0 {
		fmt.Fprintln(w, "No reviewed claims in these ledgers.")
	}
}

func pctOf(n, d int) string {
	if d == 0 {
		return "—"
	}
	return fmt.Sprintf("%d%%", (n*100+d/2)/d)
}
