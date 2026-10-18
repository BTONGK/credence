package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"credence_claim","arguments":{"type":"fix","title":"Fixed x","statement":"x passes","evidence_hints":["a.py"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"credence_claim","arguments":{"type":"magic","title":"","statement":"s"}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 responses, got %d: %s", len(lines), out.String())
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	json.Unmarshal([]byte(lines[0]), &init)
	if init.Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("negotiated %q", init.Result.ProtocolVersion)
	}
	if !strings.Contains(lines[1], `"credence_claim"`) {
		t.Errorf("tools/list: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"isError":false`) || !strings.Contains(lines[2], "Claim recorded") {
		t.Errorf("valid call: %s", lines[2])
	}
	if !strings.Contains(lines[3], `"isError":true`) || !strings.Contains(lines[3], "type must be one of") {
		t.Errorf("invalid call: %s", lines[3])
	}
}
