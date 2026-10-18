// Package claims extracts self-claims filed through credence_claim,
// reconciles them with auditor claims, and provides the session view the
// auditor and verifiers work from.
package claims

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
)

// Data is everything known about a session that later stages need.
type Data struct {
	Session     *store.SessionRow
	Events      []spec.Event
	Children    []*store.SessionRow
	ChildEvents map[string][]spec.Event
	Artifacts   []*spec.Artifact
	Commits     []*gitx.Commit
	Policy      *policy.Policy
	// PolicyEdited is set when the session changed credence.yaml.
	PolicyEdited bool

	bySeq     map[int]*spec.Event
	callIndex map[string][2]int // tool_call_id -> [call seq, result seq]
}

// Load gathers a session's data from the store.
func Load(st *store.Store, sessionID string) (*Data, error) {
	s, err := st.GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	d := &Data{Session: s, ChildEvents: map[string][]spec.Event{}}
	if d.Events, err = st.Events(sessionID, 0, 0); err != nil {
		return nil, err
	}
	if d.Children, err = st.ChildSessions(sessionID); err != nil {
		return nil, err
	}
	for _, c := range d.Children {
		evs, err := st.Events(c.ID, 0, 0)
		if err != nil {
			return nil, err
		}
		d.ChildEvents[c.ID] = evs
	}
	if d.Artifacts, err = st.SessionArtifacts(sessionID); err != nil {
		return nil, err
	}
	for _, a := range d.Artifacts {
		if a.Kind != "commit" {
			continue
		}
		var c gitx.Commit
		if json.Unmarshal(a.Meta, &c) == nil && c.SHA != "" {
			d.Commits = append(d.Commits, &c)
		}
	}
	sort.Slice(d.Commits, func(i, j int) bool { return d.Commits[i].CommitTime.Before(d.Commits[j].CommitTime) })
	d.index()
	d.Policy, d.PolicyEdited = loadPolicy(d)
	return d, nil
}

// loadPolicy reads credence.yaml. If the session itself changed the policy,
// the version from before the session applies: the claimant never sets the
// rules it is graded by.
func loadPolicy(d *Data) (*policy.Policy, bool) {
	repo := d.Session.Repo
	edited := false
	for i := range d.Events {
		e := &d.Events[i]
		if e.Kind == spec.KindFileEdit && d.RelPath(PayloadStr(e, "path")) == policy.FileName {
			edited = true
		}
	}
	for _, c := range d.Commits {
		for _, f := range c.Files {
			if f == policy.FileName {
				edited = true
			}
		}
	}
	if edited && repo != "" {
		base := d.Session.StartCommit
		if base == "" && len(d.Commits) > 0 && len(d.Commits[0].Parents) > 0 {
			base = d.Commits[0].Parents[0]
		}
		if base != "" {
			if b, err := gitx.FileAt(repo, base, policy.FileName); err == nil {
				if p, err := policy.Parse([]byte(b), policy.FileName+"@"+base); err == nil {
					return p, true
				}
			}
		}
		return policy.Default(), true
	}
	p, err := policy.Load(repo)
	if p == nil || err != nil {
		p = policy.Default()
	}
	return p, false
}

func (d *Data) index() {
	d.bySeq = map[int]*spec.Event{}
	d.callIndex = map[string][2]int{}
	for i := range d.Events {
		e := &d.Events[i]
		d.bySeq[e.Seq] = e
		id := PayloadStr(e, "tool_call_id")
		if id == "" {
			continue
		}
		pair := d.callIndex[id]
		if e.Kind == spec.KindToolResult {
			pair[1] = e.Seq
		} else if pair[0] == 0 {
			pair[0] = e.Seq
		}
		d.callIndex[id] = pair
	}
}

// Event returns the event with seq, or nil.
func (d *Data) Event(seq int) *spec.Event { return d.bySeq[seq] }

// Call returns the call and result seqs for a tool call id.
func (d *Data) Call(id string) (int, int, bool) {
	p, ok := d.callIndex[id]
	return p[0], p[1], ok && p[0] > 0
}

// Result returns the tool_result event paired with a call event.
func (d *Data) Result(call *spec.Event) *spec.Event {
	id := PayloadStr(call, "tool_call_id")
	if id == "" {
		return nil
	}
	_, r, ok := d.Call(id)
	if !ok || r == 0 {
		return nil
	}
	return d.bySeq[r]
}

// Payload decodes an event payload.
func Payload(e *spec.Event) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(e.Payload, &m)
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// PayloadStr reads one string field from an event payload.
func PayloadStr(e *spec.Event, key string) string {
	m := Payload(e)
	s, _ := m[key].(string)
	return s
}

// RelPath makes a path repo-relative where possible.
func (d *Data) RelPath(p string) string {
	if p == "" {
		return p
	}
	base := d.Session.Repo
	if base == "" {
		base = d.Session.Cwd
	}
	if filepath.IsAbs(p) && base != "" {
		if r, err := filepath.Rel(base, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(strings.TrimPrefix(p, "./"))
}

// EditOK reports whether a file_edit event's tool call succeeded.
func (d *Data) EditOK(e *spec.Event) bool {
	r := d.Result(e)
	if r == nil {
		return true // result not captured; assume applied
	}
	ok, _ := Payload(r)["ok"].(bool)
	return ok
}

// CommitBySHA finds a session commit by (short) sha.
func (d *Data) CommitBySHA(sha string) *gitx.Commit {
	for _, c := range d.Commits {
		if strings.HasPrefix(c.SHA, sha) || strings.HasPrefix(sha, c.SHA) {
			return c
		}
	}
	return nil
}

// CommitSeq returns the seq of the commit event for sha, or 0.
func (d *Data) CommitSeq(sha string) int {
	for _, e := range d.Events {
		if e.Kind == spec.KindCommit {
			s := PayloadStr(&e, "sha")
			if s != "" && (strings.HasPrefix(sha, s) || strings.HasPrefix(s, sha)) {
				return e.Seq
			}
		}
	}
	return 0
}

// SignificantEvents counts events that are not system bookkeeping.
func (d *Data) SignificantEvents() int {
	n := 0
	for _, e := range d.Events {
		switch e.Kind {
		case spec.KindSessionStart, spec.KindSessionEnd, spec.KindCompaction, spec.KindHook:
		default:
			n++
		}
	}
	return n
}

// HasEditsOrCommits reports whether the session changed anything.
func (d *Data) HasEditsOrCommits() bool {
	if len(d.Commits) > 0 {
		return true
	}
	for _, e := range d.Events {
		if e.Kind == spec.KindFileEdit || e.Kind == spec.KindCommit {
			return true
		}
	}
	for _, evs := range d.ChildEvents {
		for _, e := range evs {
			if e.Kind == spec.KindFileEdit {
				return true
			}
		}
	}
	return false
}

// Subset returns a view holding only the events keep selects, plus the
// results of any kept tool calls.
func (d *Data) Subset(keep func(seq int) bool) *Data {
	s := &Data{Session: d.Session, Children: nil, ChildEvents: map[string][]spec.Event{}, Artifacts: d.Artifacts,
		Commits: d.Commits, Policy: d.Policy}
	kept := map[int]bool{}
	for i := range d.Events {
		e := &d.Events[i]
		if keep(e.Seq) {
			kept[e.Seq] = true
			if r := d.Result(e); r != nil {
				kept[r.Seq] = true
			}
		}
	}
	for i := range d.Events {
		if kept[d.Events[i].Seq] {
			s.Events = append(s.Events, d.Events[i])
		}
	}
	s.index()
	return s
}
