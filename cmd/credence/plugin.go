package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/plugins"
	"github.com/BTONGK/credence/verifiers"
)

const pluginUsage = `list | check <name> | check -- <command…> | add <name> [-- <command…>] | remove <name>`

// cmdPlugin manages verifier plugins. Plugins live only in the user's
// config; a repository can require one (plugin:<name>) but never add one.
func cmdPlugin(ctx context.Context, args []string) error {
	// Everything after "--" is the plugin's own command line.
	var command []string
	for i, a := range args {
		if a == "--" {
			command, args = args[i+1:], args[:i]
			break
		}
	}
	fs := newFlags("plugin", pluginUsage)
	asJSON := fs.Bool("json", false, "print JSON")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		pos = []string{"list"}
	}
	reg, err := plugins.Load()
	if err != nil {
		return err
	}
	cfg := config.Load()
	switch pos[0] {
	case "list", "ls", "search":
		return pluginList(ctx, cfg, reg, *asJSON)
	case "check":
		cmdline, tmp, err := pluginCommand(cfg, reg, pos[1:], command, false)
		if err != nil {
			return err
		}
		if tmp != "" {
			defer os.RemoveAll(filepath.Dir(tmp))
		}
		steps, _ := verifiers.CheckPlugin(ctx, cmdline)
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"command": cmdline, "conforms": verifiers.Conforms(steps), "steps": steps})
		}
		printSteps(cmdline, steps)
		if !verifiers.Conforms(steps) {
			return errors.New("the plugin does not conform to the protocol (spec section 11)")
		}
		return nil
	case "add", "install":
		if len(pos) < 2 {
			return fmt.Errorf("usage: credence plugin add <name> [-- <command…>]")
		}
		name := pos[1]
		for _, p := range cfg.Plugins {
			if p.Name == name {
				return fmt.Errorf("plugin %s is already registered (remove it first)", name)
			}
		}
		cmdline, installed, err := pluginCommand(cfg, reg, pos[1:2], command, true)
		if err != nil {
			return err
		}
		steps, pl := verifiers.CheckPlugin(ctx, cmdline)
		printSteps(cmdline, steps)
		if !verifiers.Conforms(steps) || pl == nil {
			if installed != "" {
				_ = os.Remove(installed)
			}
			return errors.New("not added: the plugin failed its conformance checks")
		}
		if !*yes && !confirm(fmt.Sprintf("Run `%s` as a verifier for claims that use %s?", strings.Join(cmdline, " "), strings.Join(sortedChecks(pl), ", "))) {
			return errors.New("cancelled")
		}
		cfg.Plugins = append(cfg.Plugins, config.Plugin{Name: name, Command: cmdline})
		if err := os.MkdirAll(home.Dir(), 0o700); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s added plugin %s. Policies can now require %s.\n", green("✓"), bold(name), bold("plugin:"+pl.PluginName))
		fmt.Println(dim("Restart the daemon to load it: credence stop && credence start"))
		return nil
	case "remove", "rm", "uninstall":
		if len(pos) < 2 {
			return fmt.Errorf("usage: credence plugin remove <name>")
		}
		var kept []config.Plugin
		found := false
		for _, p := range cfg.Plugins {
			if p.Name == pos[1] {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		if !found {
			return fmt.Errorf("no plugin named %s", pos[1])
		}
		cfg.Plugins = kept
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s removed plugin %s (restart the daemon to unload it)\n", green("✓"), pos[1])
		return nil
	}
	return fmt.Errorf("unknown plugin command %q; usage: credence plugin %s", pos[0], pluginUsage)
}

// pluginCommand resolves what to run: an explicit command, a registered
// plugin, or a registry entry. A bundled entry is written to
// ~/.credence/plugins when install is set, else to a temporary directory;
// the second result is the file written, if any.
func pluginCommand(cfg *config.Config, reg *plugins.Registry, names, command []string, install bool) ([]string, string, error) {
	if len(command) > 0 {
		return command, "", nil
	}
	if len(names) == 0 {
		return nil, "", errors.New("name a plugin or give its command after --")
	}
	name := names[0]
	for _, p := range cfg.Plugins {
		if p.Name == name {
			return p.Command, "", nil
		}
	}
	e := reg.Find(name)
	if e == nil {
		return nil, "", fmt.Errorf("no plugin %q is registered or listed; give its command after --", name)
	}
	for _, r := range e.Requires {
		if _, err := exec.LookPath(r); err != nil {
			return nil, "", fmt.Errorf("plugin %s needs %s on PATH", name, r)
		}
	}
	if e.Bundled == "" {
		msg := fmt.Sprintf("%s is not bundled with Credence. Install it from %s", name, e.Source)
		if e.Install != "" {
			msg += ":\n  " + e.Install
		}
		return nil, "", fmt.Errorf("%s\nthen run: credence plugin add %s -- %s", msg, name, strings.Join(e.Command, " "))
	}
	b, err := plugins.Bundled(e.Bundled)
	if err != nil {
		return nil, "", err
	}
	dir := home.Sub("plugins")
	path := filepath.Join(dir, e.Bundled)
	if !install {
		// Check the bundled copy without installing it.
		tmp, err := os.MkdirTemp("", "credence-plugin-")
		if err != nil {
			return nil, "", err
		}
		path = filepath.Join(tmp, e.Bundled)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		return nil, "", err
	}
	return e.CommandFor(path), path, nil
}

func pluginList(ctx context.Context, cfg *config.Config, reg *plugins.Registry, asJSON bool) error {
	type row struct {
		Name    string   `json:"name"`
		Command []string `json:"command"`
		Checks  []string `json:"checks,omitempty"`
		Error   string   `json:"error,omitempty"`
	}
	var rows []row
	for _, p := range cfg.Plugins {
		r := row{Name: p.Name, Command: p.Command}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		pl, err := verifiers.LoadPlugin(c, p.Name, p.Command)
		cancel()
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Checks = sortedChecks(pl)
		}
		rows = append(rows, r)
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"registered": rows, "registry": reg.Plugins})
	}
	fmt.Println(bold("Registered plugins") + dim("  ("+tildePath(home.Config())+")"))
	if len(rows) == 0 {
		fmt.Println("  none")
	}
	for _, r := range rows {
		status := green("ok") + "  " + strings.Join(r.Checks, ", ")
		if r.Error != "" {
			status = red("error") + "  " + r.Error
		}
		fmt.Printf("  %-14s %s\n  %-14s %s\n", r.Name, status, "", dim(strings.Join(r.Command, " ")))
	}
	fmt.Println()
	fmt.Println(bold("Registry") + dim("  (credence plugin add <name>)"))
	for _, e := range reg.Plugins {
		tag := ""
		if e.Bundled != "" {
			tag = dim(" [bundled]")
		}
		fmt.Printf("  %-14s %s%s\n  %-14s %s\n", e.Name, e.Description, tag, "", dim("checks: "+strings.Join(e.Checks, ", ")))
	}
	return nil
}

func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

func printSteps(cmdline []string, steps []verifiers.CheckStep) {
	fmt.Println(bold("Plugin conformance") + dim("  "+strings.Join(cmdline, " ")))
	for _, s := range steps {
		mark := green("✓")
		switch {
		case !s.OK:
			mark = red("✗")
		case s.Warn:
			mark = yellow("!")
		}
		line := "  " + mark + " " + s.Name
		if s.Detail != "" {
			line += dim(": " + s.Detail)
		}
		fmt.Println(line)
	}
}

func sortedChecks(p *verifiers.Plugin) []string {
	var out []string
	for c := range p.Checks {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
