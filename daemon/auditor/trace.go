// Package auditor proposes claims from a session trace without ever seeing
// the agent's self-claims. A deterministic rules auditor runs locally with
// no model; a model auditor (any provider) can replace it.
package auditor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/spec"
)

// TraceOptions bounds the rendered trace.
type TraceOptions struct {
	MaxChars      int
	OmitClaims    bool // hide credence_claim calls (the auditor must be blind)
	OutputExcerpt int
	MessageChars  int
}

// RenderTrace renders a session as compact numbered lines a model can cite.
func RenderTrace(d *claims.Data, o TraceOptions) string {
	if o.OutputExcerpt == 0 {
		o.OutputExcerpt = 400
	}
	if o.MessageChars == 0 {
		o.MessageChars = 1200
	}
	claimCalls := map[string]bool{}
	var lines []string
	for i := range d.Events {
		e := &d.Events[i]
		if o.OmitClaims && e.Kind == spec.KindClaimFiled {
			claimCalls[claims.PayloadStr(e, "tool_call_id")] = true
			continue
		}
		if l := renderEvent(d, e, o, claimCalls); l != "" {
			lines = append(lines, l)
		}
	}
	for _, child := range d.Children {
		evs := d.ChildEvents[child.ID]
		edits := map[string]bool{}
		var last string
		for i := range evs {
			e := &evs[i]
			switch e.Kind {
			case spec.KindFileEdit:
				edits[d.RelPath(claims.PayloadStr(e, "path"))] = true
			case spec.KindMessage:
				last = claims.PayloadStr(e, "text")
			}
		}
		var files []string
		for f := range edits {
			files = append(files, f)
		}
		sort.Strings(files)
		lines = append(lines, fmt.Sprintf("[subagent %s type=%s events=%d edited=%s] final: %s", child.ID, orDash(child.AgentType), len(evs),
			strings.Join(files, ","), clip(last, 600)))
	}
	out := strings.Join(lines, "\n")
	if o.MaxChars > 0 && len(out) > o.MaxChars {
		out = compress(lines, o.MaxChars)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func role(actor string) string {
	switch {
	case strings.HasPrefix(actor, "human:"):
		return "human"
	case strings.HasPrefix(actor, "subagent:"):
		return "subagent"
	case strings.HasPrefix(actor, "agent:"):
		return "agent"
	case strings.HasPrefix(actor, "tool:"):
		return "tool"
	}
	return "system"
}

func renderEvent(d *claims.Data, e *spec.Event, o TraceOptions, claimCalls map[string]bool) string {
	p := claims.Payload(e)
	s := func(k string) string { v, _ := p[k].(string); return v }
	pre := fmt.Sprintf("#%d [%s] ", e.Seq, role(e.Actor))
	switch e.Kind {
	case spec.KindPrompt:
		return pre + "prompt: " + clip(s("text"), o.MessageChars)
	case spec.KindMessage:
		return pre + "message: " + clip(s("text"), o.MessageChars)
	case spec.KindReasoning:
		return pre + "thinking: " + clip(s("text"), 300)
	case spec.KindCommand:
		line := pre + "$ " + clip(s("command"), 400)
		if r := d.Result(e); r != nil {
			line += " → " + resultSummary(claims.Payload(r), o.OutputExcerpt)
		}
		return line
	case spec.KindFileEdit:
		line := fmt.Sprintf("%sedit %s %s (+%v -%v)", pre, s("op"), d.RelPath(s("path")), p["added"], p["removed"])
		if r := d.Result(e); r != nil {
			if ok, _ := claims.Payload(r)["ok"].(bool); !ok {
				line += " FAILED: " + clip(fmt.Sprint(claims.Payload(r)["output"]), 160)
			}
		}
		return line
	case spec.KindFileRead:
		return pre + "read " + d.RelPath(s("path"))
	case spec.KindSearch:
		return fmt.Sprintf("%ssearch %s %s", pre, s("tool"), clip(s("pattern"), 120))
	case spec.KindSubagentStart:
		return fmt.Sprintf("%sspawn subagent type=%s: %s", pre, orDash(s("agent_type")), clip(s("description")+" — "+s("prompt"), 500))
	case spec.KindSubagentStop:
		return pre + "subagent finished: " + clip(s("summary"), 600)
	case spec.KindCommit:
		return fmt.Sprintf("%scommit %s %q", pre, s("sha"), s("subject"))
	case spec.KindToolCall:
		in := fmt.Sprint(p["input"])
		return fmt.Sprintf("%stool %s %s", pre, s("tool"), clip(in, 300))
	case spec.KindToolResult:
		if claimCalls[s("tool_call_id")] {
			return ""
		}
		switch s("call_kind") {
		case spec.KindCommand, spec.KindFileEdit, spec.KindFileRead, spec.KindSearch, spec.KindSubagentStart:
			return "" // folded into the call line
		}
		return pre + "result: " + resultSummary(p, o.OutputExcerpt)
	case spec.KindCompaction:
		return pre + "(context compacted)"
	case spec.KindSessionStart:
		return fmt.Sprintf("%ssession start cwd=%s branch=%s", pre, d.RelPath(s("cwd")), s("branch"))
	}
	return ""
}

func resultSummary(p map[string]any, excerpt int) string {
	var parts []string
	if v, ok := p["exit_code"].(float64); ok {
		parts = append(parts, fmt.Sprintf("exit %d", int(v)))
	} else if ok, _ := p["ok"].(bool); !ok {
		parts = append(parts, "error")
	}
	if to, ok := p["test_outcome"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("tests %v: %v passed, %v failed", to["status"], to["passed"], to["failed"]))
	}
	if c, ok := p["commit"].(map[string]any); ok {
		parts = append(parts, fmt.Sprintf("created commit %v", c["sha"]))
	}
	out, _ := p["output"].(string)
	if out = strings.TrimSpace(out); out != "" {
		parts = append(parts, "output: "+clip(out, excerpt))
	}
	return strings.Join(parts, "; ")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	head := n * 2 / 3
	return s[:head] + " … " + s[len(s)-(n-head-3):]
}

// compress keeps prompts, commits, edits, test runs and messages, and
// collapses reads/searches when a trace exceeds its budget.
func compress(lines []string, max int) string {
	var keep []string
	reads := 0
	for _, l := range lines {
		if strings.Contains(l, "] read ") || strings.Contains(l, "] search ") || strings.Contains(l, "] thinking: ") {
			reads++
			continue
		}
		if reads > 0 {
			keep = append(keep, fmt.Sprintf("(… %d reads/searches omitted …)", reads))
			reads = 0
		}
		keep = append(keep, l)
	}
	out := strings.Join(keep, "\n")
	for len(out) > max {
		// Shorten the longest lines until the trace fits.
		longest := 0
		for i, l := range keep {
			if len(l) > len(keep[longest]) {
				longest = i
			}
			_ = l
		}
		if len(keep[longest]) < 200 {
			break
		}
		keep[longest] = keep[longest][:len(keep[longest])/2] + " …[trimmed]"
		out = strings.Join(keep, "\n")
	}
	if len(out) > max {
		out = out[:max/2] + "\n…[middle of session omitted]…\n" + out[len(out)-max/2:]
	}
	return out
}
