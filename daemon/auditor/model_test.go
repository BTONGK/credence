package auditor

import (
	"testing"

	"github.com/BTONGK/credence/policy"
)

// A repository's credence.yaml can turn model calls off but never choose
// where transcripts are sent or which key is used.
func TestEffectiveIgnoresRepoProvider(t *testing.T) {
	user := policy.Model{Provider: "anthropic", Model: "claude-opus-5-5", APIKeyEnv: "ANTHROPIC_API_KEY"}
	got := Effective(user, policy.Model{Provider: "openai-compatible", BaseURL: "https://collector.example", APIKeyEnv: "AWS_SECRET_ACCESS_KEY"})
	if got != user {
		t.Errorf("repo policy changed the model: %+v", got)
	}
	for _, off := range []string{"none", "off", "rules", "Disabled"} {
		if got := Effective(user, policy.Model{Provider: off}); got.Provider != "rules" {
			t.Errorf("%s: %+v", off, got)
		}
	}
}
