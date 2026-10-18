// Package plugins is the registry of known verifier plugins. To list a
// plugin, add an entry to registry.json: what it checks, where its source
// lives, how to install it and the command that runs it. Entries are
// suggestions only; a plugin runs once the user adds it to their own config,
// after `credence plugin check` passes. Plugins bundled here (bundled set)
// are written to ~/.credence/plugins by `credence plugin add <name>`.
package plugins

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed registry.json *.py
var files embed.FS

// Entry is one registry listing.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Checks      []string `json:"checks"`
	Tier        int      `json:"tier"`
	Source      string   `json:"source"`
	Install     string   `json:"install,omitempty"` // shown to the user, never run by Credence
	Bundled     string   `json:"bundled,omitempty"` // file in this package
	Command     []string `json:"command"`           // {path} is replaced by the installed file
	Requires    []string `json:"requires,omitempty"`
}

// Registry is registry.json.
type Registry struct {
	Spec    string  `json:"spec"`
	Plugins []Entry `json:"plugins"`
}

// Load parses the embedded registry.
func Load() (*Registry, error) {
	b, err := files.ReadFile("registry.json")
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Find returns the entry named name.
func (r *Registry) Find(name string) *Entry {
	for i := range r.Plugins {
		if r.Plugins[i].Name == name {
			return &r.Plugins[i]
		}
	}
	return nil
}

// Bundled returns the contents of a bundled plugin file.
func Bundled(name string) ([]byte, error) { return files.ReadFile(name) }

// CommandFor fills {path} in the entry's command.
func (e *Entry) CommandFor(path string) []string {
	out := make([]string, len(e.Command))
	for i, a := range e.Command {
		out[i] = strings.ReplaceAll(a, "{path}", path)
	}
	return out
}
