// Package capture holds the in-loop capture path: the hook command that
// harnesses run, and the spool it appends to. The hook never calls a model
// or the network and always exits 0, so a broken hook never breaks the agent.
package capture

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/internal/home"
)

// MaxStdin bounds how much hook input is read.
const MaxStdin = 8 << 20

// keep lists hook payload fields worth spooling; tool outputs are not kept
// because the transcript already has them.
var keep = []string{
	"tool_name", "tool_use_id", "source", "reason", "agent_id", "agent_type", "agent_transcript_path",
	"stop_hook_active", "model", "generation_id", "permission_mode", "file_path", "turn_id",
}

// Hook reads a native hook payload from r and appends a spool record.
// It returns nil even on malformed input; errors are only for tests.
func Hook(harness string, r io.Reader, now time.Time) error {
	if os.Getenv("CREDENCE_INTERNAL") != "" {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(r, MaxStdin))
	var raw map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &raw)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	rec := adapters.HookRecord{V: 1, Harness: harness, TS: now.UTC()}
	if a, ok := adapters.Get(harness); ok {
		rec.Event = a.HookEventName(raw)
	}
	if rec.Event == "" {
		rec.Event = adapters.Str(raw, "hook_event_name", "event", "hookEventName")
	}
	rec.SessionID = adapters.Str(raw, "session_id", "sessionId", "conversation_id", "thread_id")
	rec.TranscriptPath = adapters.Str(raw, "transcript_path", "transcriptPath", "rollout_path")
	rec.Cwd = adapters.Str(raw, "cwd")
	if rec.Cwd == "" {
		if roots := adapters.Arr(raw, "workspace_roots"); len(roots) > 0 {
			rec.Cwd, _ = roots[0].(string)
		}
	}
	if rec.Cwd == "" {
		rec.Cwd, _ = os.Getwd()
	}
	extra := map[string]any{}
	for _, k := range keep {
		if v, ok := raw[k]; ok {
			if s, isStr := v.(string); isStr && len(s) > 500 {
				continue
			}
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		rec.Extra, _ = json.Marshal(extra)
	}
	return appendSpool(harness, rec)
}

// SpoolPath returns the spool file for a harness.
func SpoolPath(harness string) string { return filepath.Join(home.Spool(), harness+".jsonl") }

func appendSpool(harness string, rec adapters.HookRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(SpoolPath(harness), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// One write call keeps concurrent appends from interleaving.
	_, err = f.Write(b)
	return err
}

// AppendRecord appends a record directly (used by the MCP server).
func AppendRecord(rec adapters.HookRecord) error { return appendSpool(rec.Harness, rec) }

// ReadSpool reads complete records from path starting at offset, returning
// the records and the new offset (a trailing partial line is left unread).
func ReadSpool(path string, offset int64) ([]adapters.HookRecord, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, nil
		}
		return nil, offset, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if st.Size() < offset {
		offset = 0 // rotated or truncated
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	br := bufio.NewReaderSize(f, 64<<10)
	var recs []adapters.HookRecord
	pos := offset
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break // partial line or EOF
		}
		pos += int64(len(line))
		var rec adapters.HookRecord
		if json.Unmarshal(line, &rec) == nil {
			recs = append(recs, rec)
		}
	}
	return recs, pos, nil
}
