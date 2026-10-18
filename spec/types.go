// Package spec defines Credence's open record formats: sessions, events,
// artifacts, claims, verdicts, attributions and ledger entries. These shapes
// are mirrored by the JSON Schemas in spec/schemas and are what other tools
// produce and consume.
package spec

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Spec identifiers, versioned independently of the CLI.
const (
	ClaimSpec   = "credence.claim/0.9"
	VerdictSpec = "credence.verdict/0.9"
	LedgerSpec  = "credence.ledger/0.9"
	EventSpec   = "credence.event/0.9"
	PolicySpec  = 1
	Version     = "0.9.2"
)

// Claim types.
const (
	TypeFix            = "fix"
	TypeFeature        = "feature"
	TypeDiagnosis      = "diagnosis"
	TypeTest           = "test"
	TypeRefactor       = "refactor"
	TypeDocs           = "docs"
	TypeReview         = "review"
	TypeResearch       = "research"
	TypeOps            = "ops"
	TypeDecision       = "decision"
	TypeNegativeResult = "negative_result"
)

// ClaimTypes lists every claim type in display order.
var ClaimTypes = []string{
	TypeFix, TypeFeature, TypeDiagnosis, TypeTest, TypeRefactor, TypeDocs,
	TypeReview, TypeResearch, TypeOps, TypeDecision, TypeNegativeResult,
}

// IsClaimType reports whether t is a known claim type.
func IsClaimType(t string) bool {
	for _, c := range ClaimTypes {
		if c == t {
			return true
		}
	}
	return false
}

// Claim origins.
const (
	OriginSelf    = "self"
	OriginAuditor = "auditor"
	OriginHuman   = "human"
	// OriginStated is a claim the agent made in prose, in a reply to the
	// human, rather than with the credence_claim tool.
	OriginStated = "stated"
)

// Event kinds.
const (
	KindSessionStart  = "session_start"
	KindPrompt        = "prompt"
	KindMessage       = "message"
	KindReasoning     = "reasoning"
	KindToolCall      = "tool_call"
	KindToolResult    = "tool_result"
	KindFileRead      = "file_read"
	KindFileEdit      = "file_edit"
	KindSearch        = "search"
	KindCommand       = "command"
	KindSubagentStart = "subagent_start"
	KindSubagentStop  = "subagent_stop"
	KindCommit        = "commit"
	KindClaimFiled    = "claim_filed"
	KindCompaction    = "compaction"
	KindSessionEnd    = "session_end"
	KindHook          = "hook"
)

// Evidence relations.
const (
	RelProduced     = "produced"
	RelDemonstrates = "demonstrates"
	RelInformed     = "informed"
)

// Verdict results.
const (
	ResultPass         = "pass"
	ResultFail         = "fail"
	ResultInconclusive = "inconclusive"
	ResultBlocked      = "blocked"
)

// Claim statuses (the outcome of the verification tiers).
const (
	StatusProposed     = "proposed"
	StatusResolved     = "resolved"
	StatusBlocked      = "blocked"
	StatusVerified     = "verified"
	StatusContradicted = "contradicted"
	StatusCorroborated = "corroborated"
	StatusOverstated   = "overstated"
	StatusUnsupported  = "unsupported"
	StatusUnverified   = "unverified"
)

// Review states (tier 3).
const (
	ReviewPending  = "pending"
	ReviewAccepted = "accepted"
	ReviewEdited   = "edited"
	ReviewDisputed = "disputed"
	ReviewRejected = "rejected"
)

// Survival states.
const (
	SurvivalSurviving = "surviving"
	SurvivalDecayed   = "decayed"
	SurvivalReverted  = "reverted"
)

// Report sections, in display order.
const (
	SectionVerified     = "verified"
	SectionUnverified   = "unverified"
	SectionContradicted = "contradicted"
	SectionAuditor      = "found_by_auditor"
	SectionFailures     = "reported_failures"
)

// DisputeReasons is the short list a dispute must pick from.
var DisputeReasons = []string{"overstated", "wrong_contributor", "not_valuable", "factually_wrong", "duplicate"}

// TokenUsage counts model tokens for a session.
type TokenUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Total returns all tokens processed.
func (t TokenUsage) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

// TranscriptRef points at the harness's own transcript file.
type TranscriptRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Lines  int    `json:"lines"`
}

// Session is one harness conversation. Subagent runs are child sessions.
type Session struct {
	ID               string        `json:"id"`
	Harness          string        `json:"harness"`
	HarnessSessionID string        `json:"harness_session_id"`
	ParentSessionID  string        `json:"parent_session_id,omitempty"`
	AgentType        string        `json:"agent_type,omitempty"`
	Repo             string        `json:"repo"`
	RepoName         string        `json:"repo_name"`
	Branch           string        `json:"branch"`
	Cwd              string        `json:"cwd"`
	Title            string        `json:"title"`
	StartedAt        time.Time     `json:"started_at"`
	EndedAt          *time.Time    `json:"ended_at,omitempty"`
	LastActivity     time.Time     `json:"last_activity"`
	State            string        `json:"state"` // open | idle | closed
	Models           []string      `json:"models"`
	Human            string        `json:"human"`
	AgentConfig      string        `json:"agent_config"`
	HarnessVersion   string        `json:"harness_version,omitempty"`
	Tokens           TokenUsage    `json:"tokens"`
	CostUSD          float64       `json:"cost_usd"`
	Transcript       TranscriptRef `json:"transcript_ref"`
	EventCount       int           `json:"event_count"`
	HeadHash         string        `json:"head_hash"`
	StartCommit      string        `json:"start_commit,omitempty"`
	Skipped          bool          `json:"skipped"`
	SkipReason       string        `json:"skip_reason,omitempty"`
	Flags            []string      `json:"flags,omitempty"`
}

// Event is one normalized step. Events are hash-chained in arrival order.
type Event struct {
	SessionID string          `json:"session_id"`
	Seq       int             `json:"seq"`
	TS        time.Time       `json:"ts"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
	// PayloadRef is the content hash of the (redacted) payload.
	PayloadRef string `json:"payload_ref"`
	// Source locates the native record the event came from.
	SrcPath string `json:"src_path,omitempty"`
	SrcLine int    `json:"src_line,omitempty"`
	Hash    string `json:"hash"`
	Prev    string `json:"prev_hash"`
}

// ChainHash computes the hash an event must carry given its predecessor.
func (e *Event) ChainHash() string {
	body := map[string]any{
		"session_id":  e.SessionID,
		"seq":         e.Seq,
		"ts":          e.TS.UTC().Format(time.RFC3339Nano),
		"kind":        e.Kind,
		"actor":       e.Actor,
		"payload_ref": e.PayloadRef,
		"prev_hash":   e.Prev,
	}
	return MustHash(body)
}

// Artifact is something outside the transcript that evidence can point to.
type Artifact struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"` // commit, file_range, test_run, ci_run, pr, issue, doc
	Locator     string          `json:"locator"`
	ContentHash string          `json:"content_hash"`
	Repo        string          `json:"repo,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Assertion is a machine-checkable predicate attached to a claim, e.g.
// {"check":"test_fails_before_passes_after","test":"...","commit":"..."}.
type Assertion map[string]any

// Check returns the assertion's check name.
func (a Assertion) Check() string { return a.Str("check") }

// Str returns a string field.
func (a Assertion) Str(k string) string {
	if v, ok := a[k]; ok {
		switch t := v.(type) {
		case string:
			return t
		case json.Number:
			return t.String()
		case float64:
			return fmt.Sprintf("%g", t)
		}
	}
	return ""
}

// Int returns an integer field or def.
func (a Assertion) Int(k string, def int) int {
	switch t := a[k].(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

// Strs returns a string-list field.
func (a Assertion) Strs(k string) []string {
	switch t := a[k].(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// Evidence is a typed pointer from a claim to an event span or artifact.
//
// Targets:
//
//	event:<session>#<from>-<to>      transcript events (inclusive)
//	git:commit/<sha>                 a commit in the session's repo
//	file:<path>@<commit>#L<a>-L<b>   a file range at a commit
//	test_run:<id>                    a test run Credence executed
//	ci:github/<owner>/<repo>/<sha>   CI status for a commit
//	pr:github/<owner>/<repo>#<n>     a pull request
//	issue:github/<owner>/<repo>#<n>  an issue
//	unresolved:<hint>                a hint that could not be resolved
type Evidence struct {
	Target      string `json:"target"`
	Relation    string `json:"relation"`
	ExcerptHash string `json:"excerpt_hash,omitempty"`
	Note        string `json:"note,omitempty"`
}

// CoContributor names another contributor to a claim.
type CoContributor struct {
	ID    string `json:"id"`
	Basis string `json:"basis,omitempty"`
}

// Claim is a falsifiable statement of contribution.
type Claim struct {
	Spec           string          `json:"spec"`
	ID             string          `json:"id"`
	SessionID      string          `json:"session_id"`
	Claimant       string          `json:"claimant"`
	Origin         string          `json:"origin"`
	Type           string          `json:"type"`
	Title          string          `json:"title"`
	Statement      string          `json:"statement"`
	Rationale      string          `json:"rationale,omitempty"`
	Assertions     []Assertion     `json:"assertions"`
	Evidence       []Evidence      `json:"evidence"`
	EvidenceHints  []string        `json:"evidence_hints,omitempty"`
	CoContributors []CoContributor `json:"co_contributors,omitempty"`
	SelfConfidence *float64        `json:"self_confidence,omitempty"`
	SourceEvent    int             `json:"source_event,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	Signature      string          `json:"signature,omitempty"`
}

// ContentHash returns the content address of the claim, excluding its id and signature.
func (c *Claim) ContentHash() string {
	cp := *c
	cp.ID = ""
	cp.Signature = ""
	// Required lists hash as [] whether or not they were ever set.
	if cp.Assertions == nil {
		cp.Assertions = []Assertion{}
	}
	if cp.Evidence == nil {
		cp.Evidence = []Evidence{}
	}
	return MustHash(cp)
}

// AssignID sets the claim's content-addressed id.
func (c *Claim) AssignID() {
	if c.Assertions == nil {
		c.Assertions = []Assertion{}
	}
	if c.Evidence == nil {
		c.Evidence = []Evidence{}
	}
	c.Spec = ClaimSpec
	c.ID = "clm_" + ShortID(c.ContentHash(), 10)
}

// Verdict is one verifier's judgment on one claim (or one assertion of it).
type Verdict struct {
	Spec       string     `json:"spec"`
	ID         string     `json:"id"`
	ClaimID    string     `json:"claim_id"`
	Verifier   string     `json:"verifier"`
	Tier       int        `json:"tier"`
	Check      string     `json:"check"`
	Result     string     `json:"result"`
	Outcome    string     `json:"outcome"`
	Details    string     `json:"details"`
	Assertion  Assertion  `json:"assertion,omitempty"`
	Artifacts  []Artifact `json:"artifacts,omitempty"`
	DurationMs int64      `json:"duration_ms"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ContentHash returns the verdict's content address.
func (v *Verdict) ContentHash() string {
	cp := *v
	cp.ID = ""
	return MustHash(cp)
}

// AssignID sets the verdict's content-addressed id.
func (v *Verdict) AssignID() {
	v.Spec = VerdictSpec
	v.ID = "vrd_" + ShortID(v.ContentHash(), 10)
}

// Share is one contributor's portion of credit for a claim.
type Share struct {
	Contributor string  `json:"contributor"`
	Share       float64 `json:"share"`
	Basis       string  `json:"basis"`
}

// Attribution splits credit for a claim (or claim cluster).
type Attribution struct {
	ClaimID string  `json:"claim_id"`
	Shares  []Share `json:"shares"`
	Method  string  `json:"method"`
}

// HumanDecision is the tier-3 outcome recorded in the ledger.
type HumanDecision struct {
	Decision        string    `json:"decision"`
	By              string    `json:"by"`
	Reason          string    `json:"reason,omitempty"`
	Note            string    `json:"note,omitempty"`
	EditedTitle     string    `json:"edited_title,omitempty"`
	EditedStatement string    `json:"edited_statement,omitempty"`
	At              time.Time `json:"at"`
}

// SurvivalCheck is a later re-check of an accepted claim.
type SurvivalCheck struct {
	Window  string  `json:"window"`
	Result  string  `json:"result"`
	Ratio   float64 `json:"ratio"`
	Details string  `json:"details"`
}

// LedgerEntry is the signed, final record of a claim decision or survival check.
type LedgerEntry struct {
	Spec          string          `json:"spec"`
	Seq           int64           `json:"seq"`
	Kind          string          `json:"kind"` // decision | survival
	ClaimID       string          `json:"claim_id"`
	ClaimHash     string          `json:"claim_hash"`
	Claim         json.RawMessage `json:"claim"`
	Status        string          `json:"status"`
	VerdictHashes []string        `json:"verdict_hashes"`
	Attribution   *Attribution    `json:"attribution,omitempty"`
	HumanDecision *HumanDecision  `json:"human_decision,omitempty"`
	Survival      *SurvivalCheck  `json:"survival,omitempty"`
	Repo          string          `json:"repo,omitempty"`
	Commits       []string        `json:"commits,omitempty"`
	AgentConfig   string          `json:"agent_config,omitempty"`
	PrevEntryHash string          `json:"prev_entry_hash"`
	EntryHash     string          `json:"entry_hash"`
	Signature     string          `json:"signature"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ComputeHash returns the hash the entry must carry (everything but entry_hash and signature).
func (l *LedgerEntry) ComputeHash() string {
	cp := *l
	cp.EntryHash = ""
	cp.Signature = ""
	return MustHash(cp)
}

// EventTarget formats an event-span evidence target.
func EventTarget(session string, from, to int) string {
	if to <= from {
		return fmt.Sprintf("event:%s#%d", session, from)
	}
	return fmt.Sprintf("event:%s#%d-%d", session, from, to)
}

// CommitTarget formats a commit evidence target.
func CommitTarget(sha string) string { return "git:commit/" + sha }

// FileTarget formats a file-range evidence target.
func FileTarget(path, commit string, from, to int) string {
	t := "file:" + path
	if commit != "" {
		t += "@" + commit
	}
	if from > 0 {
		t += fmt.Sprintf("#L%d-L%d", from, to)
	}
	return t
}

// ParsedTarget is a decomposed evidence target.
type ParsedTarget struct {
	Scheme  string // event, git, file, test_run, ci, pr, issue, unresolved
	Session string
	From    int
	To      int
	Commit  string
	Path    string
	ID      string
	Raw     string
}

// ParseTarget decomposes an evidence target string.
func ParseTarget(t string) ParsedTarget {
	p := ParsedTarget{Raw: t}
	i := strings.Index(t, ":")
	if i < 0 {
		p.Scheme = "unresolved"
		p.ID = t
		return p
	}
	p.Scheme = t[:i]
	rest := t[i+1:]
	switch p.Scheme {
	case "event":
		sess, span, _ := strings.Cut(rest, "#")
		p.Session = sess
		a, b, found := strings.Cut(span, "-")
		fmt.Sscanf(a, "%d", &p.From)
		if found {
			fmt.Sscanf(b, "%d", &p.To)
		} else {
			p.To = p.From
		}
	case "git":
		if strings.HasPrefix(rest, "commit/") {
			p.Commit = strings.TrimPrefix(rest, "commit/")
		} else {
			p.ID = rest
		}
	case "file":
		pathPart, rng, _ := strings.Cut(rest, "#")
		path, commit, _ := strings.Cut(pathPart, "@")
		p.Path, p.Commit = path, commit
		if rng != "" {
			fmt.Sscanf(rng, "L%d-L%d", &p.From, &p.To)
		}
	default:
		p.ID = rest
	}
	return p
}
