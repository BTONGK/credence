// Package policy loads a repo's credence.yaml: what counts as credit, which
// verifiers each claim type needs, survival windows and auto-accept rules.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BTONGK/credence/spec"
	"gopkg.in/yaml.v3"
)

// FileName is the policy file at the repo root.
const FileName = "credence.yaml"

// AutoAccept lets well-calibrated agent configs skip review for low-value types.
type AutoAccept struct {
	MaxBrier   float64 `yaml:"max_brier" json:"max_brier"`
	MinSamples int     `yaml:"min_samples" json:"min_samples"`
}

// ClaimType is the per-type policy.
type ClaimType struct {
	Weight      float64     `yaml:"weight" json:"weight"`
	Require     []string    `yaml:"require" json:"require"`
	HumanReview string      `yaml:"human_review" json:"human_review"` // required | optional
	AutoAccept  *AutoAccept `yaml:"auto_accept" json:"auto_accept,omitempty"`
	MaxPerSess  int         `yaml:"max_per_session" json:"max_per_session,omitempty"`
}

// Survival configures re-checks of accepted claims.
type Survival struct {
	Windows  []string `yaml:"windows" json:"windows"`
	OnRevert string   `yaml:"on_revert" json:"on_revert"` // zero_credit | keep
}

// Model configures a model-backed stage (auditor or judge).
type Model struct {
	Provider              string `yaml:"provider" json:"provider"`
	Model                 string `yaml:"model" json:"model"`
	BaseURL               string `yaml:"base_url" json:"base_url,omitempty"`
	APIKeyEnv             string `yaml:"api_key_env" json:"api_key_env,omitempty"`
	PreferDifferentFamily bool   `yaml:"prefer_different_family" json:"prefer_different_family"`
}

// Redaction adds repo-specific secret patterns.
type Redaction struct {
	ExtraPatterns []string `yaml:"extra_patterns" json:"extra_patterns"`
}

// SkipSessions defines trivial sessions that get no credit report.
type SkipSessions struct {
	MinEvents             int  `yaml:"min_events" json:"min_events"`
	RequireEditsOrCommits bool `yaml:"require_edits_or_commits" json:"require_edits_or_commits"`
}

// Tests tells the test verifier how to run tests in a clean worktree.
type Tests struct {
	// Command is a template; {test} is replaced by the shell-quoted test id.
	Command string `yaml:"command" json:"command"`
	// Setup runs once in each fresh worktree (e.g. "npm ci").
	Setup string `yaml:"setup" json:"setup"`
	// LinkDirs are dependency directories symlinked from the main checkout.
	LinkDirs       []string `yaml:"link_dirs" json:"link_dirs"`
	DefaultRuns    int      `yaml:"default_runs" json:"default_runs"`
	MaxRuns        int      `yaml:"max_runs" json:"max_runs"`
	BudgetSeconds  int      `yaml:"budget_seconds" json:"budget_seconds"`
	AllowCommands  []string `yaml:"allow_commands" json:"allow_commands"`
	TrustAgentCmds bool     `yaml:"trust_agent_commands" json:"trust_agent_commands"`
	Mutation       Mutation `yaml:"mutation" json:"mutation"`
}

// Mutation configures mutation checks of tests written in the session.
type Mutation struct {
	Enabled    *bool   `yaml:"enabled" json:"enabled,omitempty"` // default on
	MaxMutants int     `yaml:"max_mutants" json:"max_mutants"`
	MinScore   float64 `yaml:"min_score" json:"min_score"`
}

// On reports whether mutation checks run for same-session tests.
func (m Mutation) On() bool { return m.Enabled == nil || *m.Enabled }

// Ledger configures sharing of signed entries.
type Ledger struct {
	GitNotes bool `yaml:"git_notes" json:"git_notes"`
}

// Attribution tunes credit splitting.
type Attribution struct {
	DirectionShare float64 `yaml:"direction_share" json:"direction_share"`
}

// Policy is a parsed credence.yaml.
type Policy struct {
	Version      int                  `yaml:"version" json:"version"`
	ClaimTypes   map[string]ClaimType `yaml:"claim_types" json:"claim_types"`
	Survival     Survival             `yaml:"survival" json:"survival"`
	Judge        Model                `yaml:"judge" json:"judge"`
	Auditor      Model                `yaml:"auditor" json:"auditor"`
	Redaction    Redaction            `yaml:"redaction" json:"redaction"`
	SkipSessions SkipSessions         `yaml:"skip_sessions" json:"skip_sessions"`
	Tests        Tests                `yaml:"tests" json:"tests"`
	Ledger       Ledger               `yaml:"ledger" json:"ledger"`
	Attribution  Attribution          `yaml:"attribution" json:"attribution"`
	Source       string               `yaml:"-" json:"source"`
}

// Default returns the built-in policy used when a repo has no credence.yaml.
func Default() *Policy {
	return &Policy{
		Version: spec.PolicySpec,
		ClaimTypes: map[string]ClaimType{
			spec.TypeFix:            {Weight: 3, Require: []string{"resolve", "tests"}, HumanReview: "required"},
			spec.TypeFeature:        {Weight: 3, Require: []string{"resolve"}, HumanReview: "required"},
			spec.TypeDiagnosis:      {Weight: 3, Require: []string{"resolve", "judge"}, HumanReview: "required"},
			spec.TypeTest:           {Weight: 2, Require: []string{"resolve", "tests"}},
			spec.TypeRefactor:       {Weight: 2, Require: []string{"resolve"}, AutoAccept: &AutoAccept{MaxBrier: 0.12, MinSamples: 20}},
			spec.TypeDocs:           {Weight: 1, Require: []string{"resolve"}, AutoAccept: &AutoAccept{MaxBrier: 0.15, MinSamples: 20}},
			spec.TypeReview:         {Weight: 2, Require: []string{"resolve", "judge"}},
			spec.TypeResearch:       {Weight: 1, Require: []string{"resolve", "judge"}},
			spec.TypeOps:            {Weight: 2, Require: []string{"resolve"}},
			spec.TypeDecision:       {Weight: 2, Require: []string{"resolve", "judge"}, HumanReview: "required"},
			spec.TypeNegativeResult: {Weight: 1, Require: []string{"resolve", "judge"}},
		},
		Survival:     Survival{Windows: []string{"7d", "30d"}, OnRevert: "zero_credit"},
		Judge:        Model{PreferDifferentFamily: true},
		SkipSessions: SkipSessions{MinEvents: 10, RequireEditsOrCommits: true},
		Tests: Tests{
			LinkDirs:      []string{"node_modules", ".venv", "venv", "vendor"},
			DefaultRuns:   3,
			MaxRuns:       50,
			BudgetSeconds: 300,
			Mutation:      Mutation{MaxMutants: 8, MinScore: 0.6},
		},
		Attribution: Attribution{DirectionShare: 0.3},
		Source:      "default",
	}
}

// Load reads credence.yaml from repo, merged over the defaults.
func Load(repo string) (*Policy, error) {
	p := Default()
	if repo == "" {
		return p, nil
	}
	path := filepath.Join(repo, FileName)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return Parse(b, path)
}

// Parse parses policy YAML over the defaults.
func Parse(b []byte, source string) (*Policy, error) {
	p := Default()
	var user Policy
	if err := yaml.Unmarshal(b, &user); err != nil {
		return p, fmt.Errorf("%s: %w", source, err)
	}
	if user.Version != 0 && user.Version != spec.PolicySpec {
		return p, fmt.Errorf("%s: unsupported policy version %d", source, user.Version)
	}
	for name, ct := range user.ClaimTypes {
		if !spec.IsClaimType(name) {
			return p, fmt.Errorf("%s: unknown claim type %q", source, name)
		}
		base := p.ClaimTypes[name]
		if ct.Weight != 0 {
			base.Weight = ct.Weight
		}
		if ct.Require != nil {
			base.Require = ct.Require
		}
		if ct.HumanReview != "" {
			base.HumanReview = ct.HumanReview
		}
		if ct.AutoAccept != nil {
			base.AutoAccept = ct.AutoAccept
			if base.AutoAccept.MinSamples == 0 {
				base.AutoAccept.MinSamples = 20
			}
		}
		if ct.MaxPerSess != 0 {
			base.MaxPerSess = ct.MaxPerSess
		}
		p.ClaimTypes[name] = base
	}
	if len(user.Survival.Windows) > 0 {
		p.Survival.Windows = user.Survival.Windows
	}
	if user.Survival.OnRevert != "" {
		p.Survival.OnRevert = user.Survival.OnRevert
	}
	mergeModel(&p.Judge, user.Judge)
	mergeModel(&p.Auditor, user.Auditor)
	p.Redaction.ExtraPatterns = append(p.Redaction.ExtraPatterns, user.Redaction.ExtraPatterns...)
	if user.SkipSessions.MinEvents != 0 {
		p.SkipSessions.MinEvents = user.SkipSessions.MinEvents
	}
	if hasKey(b, "require_edits_or_commits") {
		p.SkipSessions.RequireEditsOrCommits = user.SkipSessions.RequireEditsOrCommits
	}
	t := user.Tests
	if t.Command != "" {
		p.Tests.Command = t.Command
	}
	if t.Setup != "" {
		p.Tests.Setup = t.Setup
	}
	if t.LinkDirs != nil {
		p.Tests.LinkDirs = t.LinkDirs
	}
	if t.DefaultRuns > 0 {
		p.Tests.DefaultRuns = t.DefaultRuns
	}
	if t.MaxRuns > 0 {
		p.Tests.MaxRuns = t.MaxRuns
	}
	if t.BudgetSeconds > 0 {
		p.Tests.BudgetSeconds = t.BudgetSeconds
	}
	if t.Mutation.Enabled != nil {
		p.Tests.Mutation.Enabled = t.Mutation.Enabled
	}
	if t.Mutation.MaxMutants > 0 {
		p.Tests.Mutation.MaxMutants = t.Mutation.MaxMutants
	}
	if t.Mutation.MinScore > 0 {
		p.Tests.Mutation.MinScore = t.Mutation.MinScore
	}
	p.Tests.AllowCommands = t.AllowCommands
	p.Tests.TrustAgentCmds = t.TrustAgentCmds
	p.Ledger = user.Ledger
	if user.Attribution.DirectionShare > 0 {
		p.Attribution.DirectionShare = user.Attribution.DirectionShare
	}
	p.Source = source
	if err := p.Validate(); err != nil {
		return p, fmt.Errorf("%s: %w", source, err)
	}
	return p, nil
}

func hasKey(b []byte, key string) bool { return strings.Contains(string(b), key+":") }

func mergeModel(dst *Model, src Model) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.APIKeyEnv != "" {
		dst.APIKeyEnv = src.APIKeyEnv
	}
	if src.PreferDifferentFamily {
		dst.PreferDifferentFamily = true
	}
}

var knownVerifiers = map[string]bool{"resolve": true, "tests": true, "mutation": true, "git": true, "ci": true, "judge": true, "human": true}

// Validate checks the policy for mistakes.
func (p *Policy) Validate() error {
	for name, ct := range p.ClaimTypes {
		for _, r := range ct.Require {
			if !knownVerifiers[r] && !strings.HasPrefix(r, "plugin:") {
				return fmt.Errorf("claim type %s requires unknown verifier %q", name, r)
			}
		}
		if ct.HumanReview != "" && ct.HumanReview != "required" && ct.HumanReview != "optional" {
			return fmt.Errorf("claim type %s: human_review must be required or optional", name)
		}
	}
	for _, w := range p.Survival.Windows {
		if _, err := ParseWindow(w); err != nil {
			return err
		}
	}
	for _, pat := range p.Redaction.ExtraPatterns {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("redaction pattern %q: %w", pat, err)
		}
	}
	return nil
}

// Type returns the policy for a claim type (zero weight 1 if unknown).
func (p *Policy) Type(t string) ClaimType {
	if ct, ok := p.ClaimTypes[t]; ok {
		return ct
	}
	return ClaimType{Weight: 1, Require: []string{"resolve"}}
}

// Requires reports whether type t requires verifier v.
func (p *Policy) Requires(t, v string) bool {
	for _, r := range p.Type(t).Require {
		if r == v {
			return true
		}
	}
	return false
}

// ParseWindow parses durations like "7d", "30d", "12h".
func ParseWindow(w string) (time.Duration, error) {
	w = strings.TrimSpace(w)
	if strings.HasSuffix(w, "d") {
		var n int
		if _, err := fmt.Sscanf(w, "%dd", &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("bad survival window %q", w)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(w)
	if err != nil {
		return 0, fmt.Errorf("bad survival window %q", w)
	}
	return d, nil
}

// Example is a commented starter credence.yaml.
const Example = `# credence.yaml — what counts as credit in this repo.
version: 1

claim_types:
  fix:             { weight: 3, require: [resolve, tests], human_review: required }
  diagnosis:       { weight: 3, require: [resolve, judge], human_review: required }
  negative_result: { weight: 1, require: [resolve, judge] }
  docs:            { weight: 1, require: [resolve], auto_accept: { max_brier: 0.15 } }

survival:
  windows: [7d, 30d]
  on_revert: zero_credit

# How the test verifier runs tests in a clean worktree. {test} is the test id.
tests:
  # command: "python -m pytest {test}"
  # setup: "npm ci"
  link_dirs: [node_modules, .venv]
  default_runs: 3
  budget_seconds: 300
  # When a fix's only passing test was written in the same session, mutate
  # the lines the fix added and check the test catches them.
  mutation:
    enabled: true
    max_mutants: 8
    min_score: 0.6

judge:
  # A repo can turn model calls off (provider: none) but never choose a model
  # or endpoint; that is set per user in ~/.credence/config.yaml.
  prefer_different_family: true

redaction:
  extra_patterns: []

skip_sessions:
  min_events: 10
  require_edits_or_commits: true

ledger:
  git_notes: false   # mirror signed entries to refs/notes/credence
`
