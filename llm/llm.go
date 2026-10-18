// Package llm runs the model-backed stages (auditor and judge) behind a
// pluggable provider: the Anthropic API, any OpenAI-compatible endpoint
// (including local models such as Ollama), or the locally installed Claude
// Code CLI. Every request receives redacted data only.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/internal/pricing"
	"github.com/BTONGK/credence/policy"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Request is one structured-output call.
type Request struct {
	System    string
	Prompt    string
	Schema    map[string]any
	MaxTokens int
	Effort    string // low | medium | high
}

// Response carries the parsed JSON and usage.
type Response struct {
	JSON      json.RawMessage
	Provider  string
	Model     string
	TokensIn  int64
	TokensOut int64
	CostUSD   float64
}

// Provider is a model backend.
type Provider interface {
	Name() string
	Model() string
	Complete(ctx context.Context, r Request) (*Response, error)
}

// ErrDisabled means no model provider is configured for a stage.
var ErrDisabled = errors.New("model provider disabled")

// DefaultModel is used when a provider has no model configured.
const DefaultModel = "claude-opus-5-5"

// New builds a provider from configuration.
func New(m policy.Model) (Provider, error) {
	switch strings.ToLower(m.Provider) {
	case "", "none", "rules", "off":
		return nil, ErrDisabled
	case "anthropic":
		model := m.Model
		if model == "" {
			model = DefaultModel
		}
		return &Anthropic{model: model, keyEnv: m.APIKeyEnv, baseURL: m.BaseURL}, nil
	case "openai-compatible", "openai", "ollama", "lmstudio", "vllm":
		base := m.BaseURL
		model := m.Model
		keyEnv := m.APIKeyEnv
		switch strings.ToLower(m.Provider) {
		case "ollama":
			if base == "" {
				base = "http://localhost:11434/v1"
			}
			if model == "" {
				model = "qwen2.5-coder:14b"
			}
		case "lmstudio":
			if base == "" {
				base = "http://localhost:1234/v1"
			}
		case "openai":
			if base == "" {
				base = "https://api.openai.com/v1"
			}
			if keyEnv == "" {
				keyEnv = "OPENAI_API_KEY"
			}
		}
		if base == "" {
			return nil, fmt.Errorf("openai-compatible provider needs base_url")
		}
		if model == "" {
			return nil, fmt.Errorf("openai-compatible provider needs model")
		}
		return &OpenAICompat{baseURL: strings.TrimRight(base, "/"), model: model, keyEnv: keyEnv, client: &http.Client{Timeout: 5 * time.Minute}}, nil
	case "claude-cli", "claude-code":
		model := m.Model
		if model == "" {
			model = DefaultModel
		}
		return &ClaudeCLI{model: model}, nil
	}
	return nil, fmt.Errorf("unknown model provider %q", m.Provider)
}

// ---- Anthropic API ----

// Anthropic calls the Claude API through the official Go SDK.
type Anthropic struct {
	model   string
	keyEnv  string
	baseURL string
}

// Name returns the provider id.
func (a *Anthropic) Name() string { return "anthropic" }

// Model returns the model id.
func (a *Anthropic) Model() string { return a.model }

// fallbackModels are the models that accept server-side refusal fallbacks
// in the "default" routing form.
var fallbackModels = map[string]bool{"claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true}

// Complete sends one request with structured output.
func (a *Anthropic) Complete(ctx context.Context, r Request) (*Response, error) {
	var opts []option.RequestOption
	if a.keyEnv != "" {
		key := os.Getenv(a.keyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s is not set", a.keyEnv)
		}
		opts = append(opts, option.WithAPIKey(key))
	}
	if a.baseURL != "" {
		opts = append(opts, option.WithBaseURL(a.baseURL))
	}
	client := anthropic.NewClient(opts...)
	maxTokens := r.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}
	effort := r.Effort
	if effort == "" {
		effort = "medium"
	}
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: int64(maxTokens),
		System:    []anthropic.BetaTextBlockParam{{Text: r.System}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(r.Prompt)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: r.Schema},
		},
	}
	if fallbackModels[a.model] {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	msg, err := client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("anthropic API %d: %s", apiErr.StatusCode, strings.TrimSpace(apiErr.Error()))
		}
		return nil, err
	}
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return nil, errors.New("model declined the request (refusal)")
	}
	if msg.StopReason == anthropic.BetaStopReasonMaxTokens {
		return nil, errors.New("model output hit max_tokens")
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if t, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	raw := json.RawMessage(extractJSON(text.String()))
	if !json.Valid(raw) {
		return nil, fmt.Errorf("model returned invalid JSON")
	}
	model := string(msg.Model)
	in := msg.Usage.InputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.CacheReadInputTokens
	return &Response{
		JSON: raw, Provider: "anthropic", Model: model, TokensIn: in, TokensOut: msg.Usage.OutputTokens,
		CostUSD: pricing.Cost(model, msg.Usage.InputTokens, msg.Usage.OutputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens),
	}, nil
}

// ---- OpenAI-compatible ----

// OpenAICompat calls any /v1/chat/completions endpoint.
type OpenAICompat struct {
	baseURL string
	model   string
	keyEnv  string
	client  *http.Client
}

// Name returns the provider id.
func (o *OpenAICompat) Name() string { return "openai-compatible" }

// Model returns the model id.
func (o *OpenAICompat) Model() string { return o.model }

// Complete sends one chat completion with a JSON schema response format,
// falling back to plain JSON mode for servers without schema support.
func (o *OpenAICompat) Complete(ctx context.Context, r Request) (*Response, error) {
	body := map[string]any{
		"model": o.model,
		"messages": []map[string]string{
			{"role": "system", "content": r.System},
			{"role": "user", "content": r.Prompt},
		},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "credence_output", "schema": r.Schema, "strict": true},
		},
	}
	if r.MaxTokens > 0 {
		body["max_tokens"] = r.MaxTokens
	}
	res, status, err := o.post(ctx, body)
	if err == nil && status == http.StatusBadRequest {
		body["response_format"] = map[string]any{"type": "json_object"}
		msgs := body["messages"].([]map[string]string)
		schema, _ := json.Marshal(r.Schema)
		msgs[0]["content"] += "\n\nRespond with a single JSON object matching this JSON Schema:\n" + string(schema)
		res, status, err = o.post(ctx, body)
	}
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d: %s", o.baseURL, status, truncate(string(res), 300))
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("no choices returned")
	}
	if out.Choices[0].Message.Refusal != "" {
		return nil, errors.New("model refused: " + out.Choices[0].Message.Refusal)
	}
	raw := json.RawMessage(extractJSON(out.Choices[0].Message.Content))
	if !json.Valid(raw) {
		return nil, fmt.Errorf("model returned invalid JSON")
	}
	model := out.Model
	if model == "" {
		model = o.model
	}
	return &Response{JSON: raw, Provider: "openai-compatible", Model: model, TokensIn: out.Usage.PromptTokens,
		TokensOut: out.Usage.CompletionTokens, CostUSD: pricing.Cost(model, out.Usage.PromptTokens, out.Usage.CompletionTokens, 0, 0)}, nil
}

func (o *OpenAICompat) post(ctx context.Context, body map[string]any) ([]byte, int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.keyEnv != "" {
		if k := os.Getenv(o.keyEnv); k != "" {
			req.Header.Set("Authorization", "Bearer "+k)
		}
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return data, resp.StatusCode, err
}

// ---- Claude Code CLI ----

// ClaudeCLI runs `claude -p` headless with tools disabled, using whatever
// login the local Claude Code already has. It sets CREDENCE_INTERNAL so
// Credence's own hooks ignore the run, and disables session persistence so
// the run leaves no transcript to capture.
type ClaudeCLI struct{ model string }

// Name returns the provider id.
func (c *ClaudeCLI) Name() string { return "claude-cli" }

// Model returns the model id.
func (c *ClaudeCLI) Model() string { return c.model }

// Complete runs one headless prompt with a JSON schema.
func (c *ClaudeCLI) Complete(ctx context.Context, r Request) (*Response, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("claude CLI not found on PATH")
	}
	schema, _ := json.Marshal(r.Schema)
	args := []string{"-p", "--output-format", "json", "--model", c.model, "--json-schema", string(schema),
		"--no-session-persistence", "--tools", "", "--strict-mcp-config", "--system-prompt", r.System}
	if r.Effort != "" {
		args = append(args, "--effort", r.Effort)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = home.Sub("auditor-work")
	cmd.Env = append(os.Environ(), "CREDENCE_INTERNAL=1")
	cmd.Stdin = strings.NewReader(r.Prompt)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude -p: %v: %s", err, truncate(errb.String()+out.String(), 400))
	}
	var res struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		TotalCostUSD     float64         `json:"total_cost_usd"`
		Usage            struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			CacheRead    int64 `json:"cache_read_input_tokens"`
			CacheWrite   int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		return nil, fmt.Errorf("claude -p output: %v", err)
	}
	if res.IsError {
		return nil, fmt.Errorf("claude -p error: %s", truncate(res.Result, 300))
	}
	raw := res.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(extractJSON(res.Result))
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("claude -p returned invalid JSON")
	}
	return &Response{JSON: raw, Provider: "claude-cli", Model: c.model,
		TokensIn: res.Usage.InputTokens + res.Usage.CacheRead + res.Usage.CacheWrite, TokensOut: res.Usage.OutputTokens, CostUSD: res.TotalCostUSD}, nil
}

// extractJSON trims code fences and surrounding prose from a JSON answer.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	if json.Valid([]byte(s)) {
		return s
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
