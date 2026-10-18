package attribution

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/BTONGK/credence/spec"
)

func TestRoundShares(t *testing.T) {
	for _, in := range [][]float64{{1.0 / 3, 1.0 / 3, 1.0 / 3}, {0.666, 0.334}, {0.125, 0.125, 0.75}, {1}} {
		s := make([]spec.Share, len(in))
		for i, v := range in {
			s[i].Share = v
		}
		roundShares(s)
		sum := 0.0
		for _, x := range s {
			if x.Share != math.Round(x.Share*100)/100 {
				t.Errorf("%v: share %v is not rounded to 0.01", in, x.Share)
			}
			sum += x.Share
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("%v: shares sum to %v", in, sum)
		}
	}
}

func TestMergeKeepsCapturedLines(t *testing.T) {
	meta, _ := json.Marshal(ImportedProvenance{Source: "agent-trace", Commit: "abc", Lines: map[string]string{
		"h1": "agent:cursor/gpt-5", "h2": "human:maya",
	}})
	p := Provenance{"h1": "agent:claude-code/claude-opus-5-5"}
	p.Merge([]*spec.Artifact{{Kind: "line_provenance", Meta: meta}, {Kind: "line_provenance", Meta: json.RawMessage(`not json`)}})
	if p["h1"] != "agent:claude-code/claude-opus-5-5" {
		t.Errorf("captured line was overwritten: %q", p["h1"])
	}
	if p["h2"] != "human:maya" {
		t.Errorf("imported line missing: %q", p["h2"])
	}
}
