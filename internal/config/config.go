// Package config holds user-level settings in ~/.credence/config.yaml.
// Repo-level rules live in credence.yaml (package policy).
package config

import (
	"os"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/policy"
	"gopkg.in/yaml.v3"
)

// Capture controls what the daemon captures.
type Capture struct {
	// Since is the earliest transcript modification time captured
	// automatically; older transcripts need `credence backfill`.
	Since time.Time `yaml:"since"`
	// IdleSeconds after a turn ends before a provisional report runs.
	IdleSeconds int `yaml:"idle_seconds"`
	// CloseMinutes of inactivity before a session is closed.
	CloseMinutes int `yaml:"close_minutes"`
	// Harnesses to watch; empty means all detected.
	Harnesses []string `yaml:"harnesses"`
	// ExcludeRepos are repo roots never captured.
	ExcludeRepos []string `yaml:"exclude_repos"`
	// ExtraRoots are additional transcript directories to watch.
	ExtraRoots []string `yaml:"extra_roots"`
}

// Budget caps model spend.
type Budget struct {
	MonthlyUSD       float64 `yaml:"monthly_usd"`
	MaxSessionTokens int     `yaml:"max_session_tokens"`
}

// Config is the user configuration.
type Config struct {
	Port    int          `yaml:"port"`
	Human   string       `yaml:"human"`
	Auditor policy.Model `yaml:"auditor"`
	Judge   policy.Model `yaml:"judge"`
	Capture Capture      `yaml:"capture"`
	Budget  Budget       `yaml:"budget"`
	// Prices overrides USD per million tokens: model-prefix -> [input, output].
	Prices map[string][2]float64 `yaml:"prices"`
	// TeamRollups enables per-person views (opt-in).
	PersonViews bool `yaml:"person_views"`
	Workers     int  `yaml:"workers"`
	// Plugins are external verifiers (JSON-RPC over stdio). Only the user
	// config can register them; a repository's credence.yaml cannot.
	Plugins []Plugin `yaml:"plugins"`
	// Autostart lets the capture hook start the daemon if it is not running.
	Autostart *bool `yaml:"autostart"`
}

// Plugin registers an external verifier.
type Plugin struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
}

// Default returns the defaults: local only, no model calls.
func Default() *Config {
	return &Config{
		Port:    7171,
		Auditor: policy.Model{Provider: "rules"},
		Judge:   policy.Model{Provider: "none", PreferDifferentFamily: true},
		Capture: Capture{IdleSeconds: 45, CloseMinutes: 10},
		Budget:  Budget{MonthlyUSD: 20, MaxSessionTokens: 120000},
		Workers: 2,
	}
}

// Load reads config.yaml over the defaults.
func Load() *Config {
	c := Default()
	b, err := os.ReadFile(home.Config())
	if err == nil {
		_ = yaml.Unmarshal(b, c)
	}
	if c.Port == 0 {
		c.Port = 7171
	}
	if c.Capture.IdleSeconds <= 0 {
		c.Capture.IdleSeconds = 45
	}
	if c.Capture.CloseMinutes <= 0 {
		c.Capture.CloseMinutes = 10
	}
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Auditor.Provider == "" {
		c.Auditor.Provider = "rules"
	}
	if c.Judge.Provider == "" {
		c.Judge.Provider = "none"
	}
	if v := os.Getenv("CREDENCE_PORT"); v != "" {
		var p int
		if _, err := fmtSscan(v, &p); err == nil && p > 0 {
			c.Port = p
		}
	}
	return c
}

// Save writes config.yaml.
func (c *Config) Save() error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(home.Config(), b, 0o600)
}

// Set assigns a dotted key, e.g. "auditor.provider".
func (c *Config) Set(key, value string) bool {
	switch strings.ToLower(key) {
	case "port":
		var p int
		if _, err := fmtSscan(value, &p); err != nil {
			return false
		}
		c.Port = p
	case "human":
		c.Human = value
	case "auditor.provider":
		c.Auditor.Provider = value
	case "auditor.model":
		c.Auditor.Model = value
	case "auditor.base_url":
		c.Auditor.BaseURL = value
	case "auditor.api_key_env":
		c.Auditor.APIKeyEnv = value
	case "judge.provider":
		c.Judge.Provider = value
	case "judge.model":
		c.Judge.Model = value
	case "judge.base_url":
		c.Judge.BaseURL = value
	case "judge.api_key_env":
		c.Judge.APIKeyEnv = value
	case "budget.monthly_usd":
		var f float64
		if _, err := fmtSscan(value, &f); err != nil {
			return false
		}
		c.Budget.MonthlyUSD = f
	case "capture.idle_seconds":
		var n int
		if _, err := fmtSscan(value, &n); err != nil {
			return false
		}
		c.Capture.IdleSeconds = n
	case "person_views":
		c.PersonViews = value == "true" || value == "1" || value == "yes"
	default:
		return false
	}
	return true
}
