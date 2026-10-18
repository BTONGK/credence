package capture

import (
	"strings"
	"testing"
	"time"

	_ "github.com/BTONGK/credence/adapters/claudecode"
)

func TestHookAppendsSmallRecord(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	in := `{"session_id":"abc","transcript_path":"/x/abc.jsonl","cwd":"/repo","hook_event_name":"PostToolUse",
"tool_name":"Bash","tool_use_id":"toolu_1","tool_response":{"stdout":"` + strings.Repeat("x", 10000) + `"}}`
	if err := Hook("claude-code", strings.NewReader(in), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := Hook("claude-code", strings.NewReader("not json"), time.Unix(101, 0)); err != nil {
		t.Fatal(err)
	}
	recs, off, err := ReadSpool(SpoolPath("claude-code"), 0)
	if err != nil || len(recs) != 2 || off == 0 {
		t.Fatalf("recs=%d off=%d err=%v", len(recs), off, err)
	}
	r := recs[0]
	if r.Event != "PostToolUse" || r.SessionID != "abc" || r.TranscriptPath != "/x/abc.jsonl" {
		t.Fatalf("bad record %+v", r)
	}
	if strings.Contains(string(r.Extra), "xxxx") || !strings.Contains(string(r.Extra), "toolu_1") {
		t.Fatalf("extra should keep ids and drop outputs: %s", r.Extra)
	}
	more, off2, _ := ReadSpool(SpoolPath("claude-code"), off)
	if len(more) != 0 || off2 != off {
		t.Fatalf("expected nothing new")
	}
}

func TestHookSkipsInternalSessions(t *testing.T) {
	t.Setenv("CREDENCE_HOME", t.TempDir())
	t.Setenv("CREDENCE_INTERNAL", "1")
	_ = Hook("claude-code", strings.NewReader(`{"session_id":"x"}`), time.Now())
	recs, _, _ := ReadSpool(SpoolPath("claude-code"), 0)
	if len(recs) != 0 {
		t.Fatal("internal session should not be spooled")
	}
}
