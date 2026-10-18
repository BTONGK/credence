// Package pricing estimates model cost from token counts.
package pricing

import (
	"sort"
	"strings"
)

// Rate is USD per million tokens.
type Rate struct {
	Input, Output, CacheRead, CacheWrite float64
}

// table is keyed by model-id prefix; longest prefix wins.
var table = map[string]Rate{
	"claude-fable-5":    {10, 50, 1.0, 12.5},
	"claude-mythos-5":   {10, 50, 1.0, 12.5},
	"claude-opus-5-5":   {4, 20, 0.20, 5},
	"claude-opus-5":     {5, 25, 0.50, 6.25},
	"claude-opus-4":     {5, 25, 0.50, 6.25},
	"claude-sonnet-5":   {2, 10, 0.20, 2.5},
	"claude-sonnet-4":   {3, 15, 0.30, 3.75},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01, 0.125},
	"claude-haiku-4":    {1, 5, 0.10, 1.25},
	"claude-3-5-sonnet": {3, 15, 0.30, 3.75},
	"claude-3-5-haiku":  {0.8, 4, 0.08, 1},
	"gpt-5":             {1.25, 10, 0.125, 0},
	"gpt-5-mini":        {0.25, 2, 0.025, 0},
	"gpt-4.1":           {2, 8, 0.5, 0},
	"gpt-4o":            {2.5, 10, 1.25, 0},
	"o3":                {2, 8, 0.5, 0},
	"o4-mini":           {1.1, 4.4, 0.275, 0},
}

// Overrides are user-configured prices (prefix -> [input, output]).
var Overrides = map[string][2]float64{}

// Lookup returns the rate for a model id, and whether it was known.
func Lookup(model string) (Rate, bool) {
	m := strings.ToLower(model)
	best := ""
	for p := range Overrides {
		if strings.HasPrefix(m, p) && len(p) > len(best) {
			best = p
		}
	}
	if best != "" {
		o := Overrides[best]
		return Rate{Input: o[0], Output: o[1], CacheRead: o[0] / 10, CacheWrite: o[0] * 1.25}, true
	}
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if strings.HasPrefix(m, k) {
			return table[k], true
		}
	}
	return Rate{}, false
}

// Cost estimates USD for token counts.
func Cost(model string, in, out, cacheRead, cacheWrite int64) float64 {
	r, ok := Lookup(model)
	if !ok {
		return 0
	}
	return (float64(in)*r.Input + float64(out)*r.Output + float64(cacheRead)*r.CacheRead + float64(cacheWrite)*r.CacheWrite) / 1e6
}

// Family returns a coarse model family used to prefer a judge from a
// different family than the claimant.
func Family(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"), strings.Contains(m, "anthropic"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"), strings.HasPrefix(m, "o4"), strings.Contains(m, "codex"), strings.Contains(m, "openai"):
		return "openai"
	case strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(m, "llama"):
		return "meta"
	case strings.Contains(m, "qwen"):
		return "qwen"
	case strings.Contains(m, "mistral"), strings.Contains(m, "codestral"):
		return "mistral"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	}
	return m
}
