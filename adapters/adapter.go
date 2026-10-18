// Package adapters defines the harness adapter contract and shared parsing
// helpers. Each harness (Claude Code, Codex, Cursor, ...) gets one adapter
// that installs capture hooks, locates transcripts and turns native records
// into normalized events.
package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Scope is where hooks are installed.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeLocal   Scope = "local"
)

// Detection reports whether a harness is installed.
type Detection struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// InstallOptions configures Install.
type InstallOptions struct {
	Scope      Scope
	Repo       string // for project/local scope
	Binary     string // absolute path of the credence binary
	WithMCP    bool
	DryRun     bool
	SkipMCPCLI bool // write config files directly instead of shelling out
}

// InstallReport describes what Install or Uninstall changed.
type InstallReport struct {
	Harness string   `json:"harness"`
	Files   []string `json:"files"`
	Actions []string `json:"actions"`
	Warn    []string `json:"warnings,omitempty"`
}

// Health is one doctor() check.
type Health struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Boundaries declares how a harness signals session start and end.
type Boundaries struct {
	Start       []string      `json:"start"`
	End         []string      `json:"end"`
	TurnEnd     []string      `json:"turn_end"`
	IdleTimeout time.Duration `json:"idle_timeout"`
}

// Capabilities declares which event kinds an adapter can provide, so the UI
// can say what's missing instead of guessing.
type Capabilities struct {
	EventKinds    []string `json:"event_kinds"`
	LineRanges    bool     `json:"line_ranges"`
	SubagentLinks bool     `json:"subagent_links"`
	TokenCounts   bool     `json:"token_counts"`
	SelfClaims    bool     `json:"self_claims"`
	Hooks         bool     `json:"hooks"`
}

// Draft is a normalized event before it is redacted, attributed and chained.
type Draft struct {
	TS      time.Time
	Kind    string
	Role    string // human | agent | subagent | tool | system
	Payload map[string]any
	SrcLine int
}

// Meta is session metadata discovered while parsing.
type Meta struct {
	HarnessSessionID string
	// Harness overrides the adapter name for generic transcripts that name
	// their source (e.g. imported OTLP telemetry from claude-code).
	Harness   string
	ParentID  string // harness id of the parent session (subagents)
	AgentType string
	Cwd       string
	Branch    string
	Model     string
	Version   string
	Title     string
	StartedAt time.Time
	// Tokens are added to the session's running totals.
	TokensIn, TokensOut, CacheRead, CacheWrite int64
	// AbsoluteTokens replaces totals instead of adding (cumulative counters).
	AbsoluteTokens bool
	// Internal marks sessions Credence itself started (e.g. the auditor).
	Internal bool
}

// Parsed is the result of parsing one native record.
type Parsed struct {
	Drafts []Draft
	Meta   Meta
}

// Parser holds per-transcript state while records are fed in order.
type Parser interface {
	// Parse handles one line (1-based lineNo) of a transcript.
	Parse(line []byte, lineNo int) (Parsed, error)
}

// HookRecord is what a capture hook appends to the spool.
type HookRecord struct {
	V              int             `json:"v"`
	Harness        string          `json:"harness"`
	Event          string          `json:"event"`
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	TS             time.Time       `json:"ts"`
	Extra          json.RawMessage `json:"extra,omitempty"`
}

// Adapter is the contract every harness adapter implements.
type Adapter interface {
	Name() string
	Detect() Detection
	Install(opts InstallOptions) (InstallReport, error)
	Uninstall(opts InstallOptions) (InstallReport, error)
	Doctor(opts InstallOptions) []Health
	// TranscriptRoots are directories watch() scans.
	TranscriptRoots() []string
	// IsTranscript reports whether path is one of this harness's transcripts.
	IsTranscript(path string) bool
	// LocateTranscript maps a hook record to a transcript path.
	LocateTranscript(h HookRecord) string
	// NewParser returns a parser for one transcript file.
	NewParser(path string) Parser
	Boundaries() Boundaries
	Capabilities() Capabilities
	// HookEventName normalizes a native hook payload's event name.
	HookEventName(raw map[string]any) string
}

var registry = map[string]Adapter{}

// Register adds an adapter.
func Register(a Adapter) { registry[a.Name()] = a }

// Get returns an adapter by name.
func Get(name string) (Adapter, bool) {
	a, ok := registry[name]
	return a, ok
}

// All returns registered adapters sorted by name.
func All() []Adapter {
	var res []Adapter
	for _, a := range registry {
		res = append(res, a)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name() < res[j].Name() })
	return res
}

// ForPath finds the adapter that owns a transcript path.
func ForPath(path string) (Adapter, bool) {
	for _, a := range All() {
		if a.IsTranscript(path) {
			return a, true
		}
	}
	return nil, false
}

// ManagedTag marks hook entries Credence owns so they can be removed cleanly.
const ManagedTag = "# credence-managed"

// IsManaged reports whether a hook command string belongs to Credence.
func IsManaged(cmd string) bool { return strings.Contains(cmd, ManagedTag) }

// HookCommand is the command line registered with a harness.
func HookCommand(binary, harness string) string {
	return shellQuote(binary) + " hook " + harness + " " + ManagedTag
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// UnderDir reports whether path is inside dir.
func UnderDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// ReadJSONFile reads a JSON object file, returning an empty map if missing.
func ReadJSONFile(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]any{}, nil
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// WriteJSONFile writes a JSON object atomically, keeping a one-time backup.
func WriteJSONFile(path string, m map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		bak := path + ".credence-backup"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			if b, err := os.ReadFile(path); err == nil {
				_ = os.WriteFile(bak, b, 0o600)
			}
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Str reads a string field from a decoded JSON object.
func Str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// Obj reads an object field.
func Obj(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

// Arr reads an array field.
func Arr(m map[string]any, k string) []any {
	if v, ok := m[k].([]any); ok {
		return v
	}
	return nil
}

// Num reads a numeric field.
func Num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}

// ParseTime parses common timestamp formats.
func ParseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// Truncate shortens s to at most n bytes keeping head and tail.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head := n * 2 / 3
	tail := n - head - 40
	if tail < 0 {
		tail = 0
	}
	return s[:head] + "\n…[" + itoa(len(s)-head-tail) + " bytes elided]…\n" + s[len(s)-tail:]
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
