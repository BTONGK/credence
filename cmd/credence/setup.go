package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/llm"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/verifiers"
	"gopkg.in/yaml.v3"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func sortedAdapters() []adapters.Adapter {
	as := adapters.All()
	sort.Slice(as, func(i, j int) bool { return as[i].Name() < as[j].Name() })
	return as
}

func cmdInit(ctx context.Context, args []string) error {
	fs := newFlags("init", "[--scope user|project|local] [--harness h,...] [--no-mcp] [--no-service] [--dry-run] [--yes]")
	scope := fs.String("scope", "user", "where to install hooks: user (all repos), project (committed .claude/settings.json), local (this checkout only)")
	harness := fs.String("harness", "", "comma-separated harnesses (default: all detected)")
	noMCP := fs.Bool("no-mcp", false, "do not register the credence_claim MCP tool")
	noService := fs.Bool("no-service", false, "do not install the background service")
	dry := fs.Bool("dry-run", false, "show what would change")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	withPolicy := fs.Bool("policy", false, "write an example credence.yaml into this repo")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	repo := gitx.Root(cwd)
	sc := adapters.Scope(*scope)
	if sc != adapters.ScopeUser && repo == "" {
		return errors.New("--scope project/local must be run inside a git repository")
	}
	want := map[string]bool{}
	for _, h := range strings.Split(*harness, ",") {
		if h = strings.TrimSpace(h); h != "" {
			want[h] = true
		}
	}
	var targets []adapters.Adapter
	fmt.Println(bold("Credence setup"))
	for _, a := range sortedAdapters() {
		if a.Name() == "generic" {
			continue
		}
		det := a.Detect()
		status := dim("not found")
		if det.Installed {
			status = green("found") + " " + dim(det.Version)
		}
		fmt.Printf("  %-12s %s\n", a.Name(), status)
		if (len(want) == 0 && det.Installed) || want[a.Name()] {
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		return errors.New("no supported harness found (Claude Code, Codex or Cursor); use --harness to force one")
	}
	fmt.Println()
	fmt.Println("This will:")
	for _, a := range targets {
		where := "your user settings"
		if sc != adapters.ScopeUser {
			where = string(sc) + " settings in " + repo
		}
		fmt.Printf("  • add capture hooks for %s to %s (marked `# credence-managed`, a backup is kept)\n", a.Name(), where)
		if !*noMCP {
			fmt.Printf("  • register the credence_claim MCP tool with %s\n", a.Name())
		}
	}
	if !*noService {
		fmt.Printf("  • install a background service (%s) that runs `credence daemon`\n", serviceKind())
	}
	fmt.Printf("  • keep all data local in %s; no model calls unless you configure an auditor or judge\n", home.Dir())
	if *dry {
		fmt.Println()
	} else if !*yes && !confirm("\nProceed?") {
		return errors.New("cancelled")
	}
	bin := exe()
	for _, a := range targets {
		rep, err := a.Install(adapters.InstallOptions{Scope: sc, Repo: repo, Binary: bin, WithMCP: !*noMCP, DryRun: *dry})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", red("✗"), a.Name(), err)
			continue
		}
		for _, act := range rep.Actions {
			if *dry {
				fmt.Printf("  %s %s\n", dim("→"), wouldDo(act))
				continue
			}
			fmt.Printf("  %s %s\n", green("✓"), act)
		}
		for _, w := range rep.Warn {
			fmt.Printf("  %s %s\n", yellow("!"), w)
		}
	}
	if *dry {
		fmt.Println(dim("\nDry run: everything above is a preview. Run `credence init` to apply it."))
		return nil
	}
	cfg := config.Load()
	if _, err := os.Stat(home.Config()); os.IsNotExist(err) {
		if cfg.Human == "" && repo != "" {
			if n := gitx.UserName(repo); n != "" {
				cfg.Human = strings.ToLower(strings.Fields(n)[0])
			}
		}
		cfg.Capture.Since = time.Now().UTC()
		if err := cfg.Save(); err == nil {
			fmt.Printf("  %s wrote %s (capturing sessions from now on; `credence backfill` imports older ones)\n", green("✓"), home.Config())
		}
	}
	if _, err := keys.Load(); err == nil {
		fmt.Printf("  %s ledger signing key in %s\n", green("✓"), home.Keys())
	}
	if *withPolicy && repo != "" {
		p := filepath.Join(repo, policy.FileName)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte(policy.Example), 0o644); err == nil {
				fmt.Printf("  %s wrote %s\n", green("✓"), p)
			}
		}
	}
	if !*noService {
		if err := installService(bin); err != nil {
			fmt.Printf("  %s service: %v (run `credence start` instead)\n", yellow("!"), err)
		} else {
			fmt.Printf("  %s background service installed\n", green("✓"))
		}
	}
	if _, err := startBackground(); err != nil {
		fmt.Printf("  %s daemon: %v\n", yellow("!"), err)
	} else {
		fmt.Printf("  %s daemon running\n", green("✓"))
	}
	fmt.Println("\nNext: use your agent as usual, then run `credence ui` to read the reports. Try `credence demo` to see a full example.")
	return nil
}

func serviceKind() string {
	switch runtime.GOOS {
	case "darwin":
		return "launchd agent dev.credence.daemon"
	case "linux":
		return "systemd user unit credenced.service"
	}
	return "none on this OS"
}

func launchdPlist() string {
	return filepath.Join(home.UserHome(), "Library", "LaunchAgents", "dev.credence.daemon.plist")
}

func systemdUnit() string {
	return filepath.Join(home.UserHome(), ".config", "systemd", "user", "credenced.service")
}

func installService(bin string) error {
	switch runtime.GOOS {
	case "darwin":
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.credence.daemon</string>
  <key>ProgramArguments</key><array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, bin, home.Path("logs", "credenced.out"), home.Path("logs", "credenced.out"))
		p := launchdPlist()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
			return err
		}
		_ = exec.Command("launchctl", "unload", p).Run()
		return exec.Command("launchctl", "load", "-w", p).Run()
	case "linux":
		unit := fmt.Sprintf("[Unit]\nDescription=Credence daemon\n\n[Service]\nExecStart=%s daemon\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n", bin)
		p := systemdUnit()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
			return err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return exec.Command("systemctl", "--user", "enable", "--now", "credenced.service").Run()
	}
	return fmt.Errorf("no service manager support on %s", runtime.GOOS)
}

func removeService() []string {
	var done []string
	switch runtime.GOOS {
	case "darwin":
		p := launchdPlist()
		if _, err := os.Stat(p); err == nil {
			_ = exec.Command("launchctl", "unload", "-w", p).Run()
			_ = os.Remove(p)
			done = append(done, "removed "+p)
		}
	case "linux":
		p := systemdUnit()
		if _, err := os.Stat(p); err == nil {
			_ = exec.Command("systemctl", "--user", "disable", "--now", "credenced.service").Run()
			_ = os.Remove(p)
			done = append(done, "removed "+p)
		}
	}
	return done
}

func cmdUninstall(ctx context.Context, args []string) error {
	fs := newFlags("uninstall", "[--purge] [--yes]")
	purge := fs.Bool("purge", false, "also delete ~/.credence (sessions, ledger, keys)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	scope := fs.String("scope", "user", "hook scope to remove")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if !*yes && !confirm("Remove Credence hooks, MCP registration and the background service?") {
		return errors.New("cancelled")
	}
	cwd, _ := os.Getwd()
	repo := gitx.Root(cwd)
	for _, a := range sortedAdapters() {
		rep, err := a.Uninstall(adapters.InstallOptions{Scope: adapters.Scope(*scope), Repo: repo, Binary: exe(), WithMCP: true})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", yellow("!"), a.Name(), err)
			continue
		}
		for _, act := range rep.Actions {
			fmt.Printf("  %s %s\n", green("✓"), act)
		}
	}
	for _, s := range removeService() {
		fmt.Printf("  %s %s\n", green("✓"), s)
	}
	_ = cmdStop(ctx, nil)
	if *purge {
		if !*yes && !confirm(fmt.Sprintf("Delete %s permanently (ledger and signing key included)?", home.Dir())) {
			return nil
		}
		if err := os.RemoveAll(home.Dir()); err != nil {
			return err
		}
		fmt.Printf("  %s deleted %s\n", green("✓"), home.Dir())
	}
	return nil
}

type check struct {
	Name   string `json:"check"`
	OK     bool   `json:"ok"`
	Warn   bool   `json:"warn,omitempty"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func cmdDoctor(ctx context.Context, args []string) error {
	fs := newFlags("doctor", "[--json]")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var cs []check
	add := func(name string, ok bool, detail, fix string) {
		cs = append(cs, check{Name: name, OK: ok, Detail: detail, Fix: fix})
	}
	warn := func(name, detail, fix string) {
		cs = append(cs, check{Name: name, OK: true, Warn: true, Detail: detail, Fix: fix})
	}
	add("binary", true, exe(), "")
	if err := os.MkdirAll(home.Dir(), 0o700); err != nil {
		add("state dir", false, err.Error(), "check permissions on "+home.Dir())
	} else {
		add("state dir", true, home.Dir(), "")
	}
	_, gitErr := exec.LookPath("git")
	add("git", gitErr == nil, "used for commit evidence and test re-runs", "install git")
	d, err := open()
	if err != nil {
		add("database", false, err.Error(), "")
	} else {
		defer d.Close()
		add("database", true, home.DB(), "")
		add("ledger key", true, "key "+d.Signer.KeyID, "")
		es, _ := d.Store.LedgerEntries(store.LedgerFilter{})
		rep := ledger.VerifyChain(es, d.Signer.Pub)
		add("ledger chain", rep.OK, fmt.Sprintf("%d entries", rep.Entries), "run `credence ledger --verify` for details")
		rows, _ := d.Store.ListSessions(store.SessionFilter{Limit: 20})
		broken := 0
		for _, r := range rows {
			evs, _ := d.Store.Events(r.ID, 0, 0)
			if len(evs) > 0 && !verifiers.ChainIntact(evs) {
				broken++
			}
		}
		add("event chains", broken == 0, fmt.Sprintf("%d recent sessions checked, %d broken", len(rows), broken), "")
		hooks, _ := d.Store.HookStats(time.Now().Add(-7 * 24 * time.Hour))
		for _, a := range sortedAdapters() {
			if a.Name() == "generic" || !a.Detect().Installed {
				continue
			}
			for _, h := range a.Doctor(adapters.InstallOptions{Scope: adapters.ScopeUser, Binary: exe(), WithMCP: true}) {
				add(a.Name()+": "+h.Check, h.OK, h.Detail, h.Fix)
			}
			n := 0
			for _, c := range hooks[a.Name()] {
				n += c
			}
			last := d.Store.LastHook(a.Name())
			if n == 0 {
				warn(a.Name()+": hook activity", "no hook events in the last 7 days", "use "+a.Name()+" for a session, then re-run doctor")
			} else {
				add(a.Name()+": hook activity", true, fmt.Sprintf("%d hook events in 7 days, last %s", n, ago(last)), "")
			}
		}
		for _, stage := range []struct {
			name string
			m    policy.Model
		}{{"auditor", d.Cfg.Auditor}, {"judge", d.Cfg.Judge}} {
			p, err := llm.New(stage.m)
			switch {
			case errors.Is(err, llm.ErrDisabled):
				warn(stage.name, "no model ("+orDash(stage.m.Provider)+"); deterministic checks only", "credence config set "+stage.name+".provider anthropic|ollama|claude-cli")
			case err != nil:
				add(stage.name, false, err.Error(), "fix "+stage.name+" settings in "+home.Config())
			default:
				add(stage.name, true, p.Name()+" "+p.Model(), "")
			}
		}
		spend := d.Store.MonthSpend()
		add("model budget", spend < d.Cfg.Budget.MonthlyUSD || d.Cfg.Budget.MonthlyUSD == 0, fmt.Sprintf("$%.2f of $%.2f this month", spend, d.Cfg.Budget.MonthlyUSD), "credence config set budget.monthly_usd N")
		jc := d.Store.JobCounts()
		if jc["failed"] > 0 {
			warn("jobs", fmt.Sprintf("%d failed jobs", jc["failed"]), "see "+home.Path("logs", "credenced.log"))
		}
	}
	if i, err := server.ReadInfo(); err != nil {
		add("daemon", false, err.Error(), "credence start (or `credence init` to install the service)")
	} else if err := i.Ping(); err != nil {
		add("daemon", false, err.Error(), "credence stop && credence start")
	} else {
		add("daemon", true, fmt.Sprintf("pid %d on %s", i.PID, i.URL()), "")
	}
	if *asJSON {
		return printJSON(cs)
	}
	bad := 0
	for _, c := range cs {
		mark := green("✓")
		switch {
		case !c.OK:
			mark = red("✗")
			bad++
		case c.Warn:
			mark = yellow("!")
		}
		fmt.Printf("%s %-32s %s\n", mark, c.Name, dim(c.Detail))
		if (!c.OK || c.Warn) && c.Fix != "" {
			fmt.Printf("  %s %s\n", dim("fix:"), c.Fix)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d checks failed", bad)
	}
	return nil
}

func cmdConfig(ctx context.Context, args []string) error {
	cfg := config.Load()
	if len(args) == 0 || args[0] == "show" {
		b, _ := yaml.Marshal(cfg)
		fmt.Printf("# %s\n%s", home.Config(), b)
		return nil
	}
	switch args[0] {
	case "get":
		if len(args) != 2 {
			return flag2("usage: credence config get key")
		}
		b, _ := yaml.Marshal(cfg)
		var m map[string]any
		_ = yaml.Unmarshal(b, &m)
		var cur any = m
		for _, k := range strings.Split(args[1], ".") {
			mm, ok := cur.(map[string]any)
			if !ok {
				return fmt.Errorf("no key %s", args[1])
			}
			cur = mm[k]
		}
		fmt.Println(cur)
		return nil
	case "set":
		if len(args) != 3 {
			return flag2("usage: credence config set key value")
		}
		if !cfg.Set(args[1], args[2]) {
			return fmt.Errorf("unknown or invalid key %s", args[1])
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("%s = %s\n", args[1], args[2])
		if strings.HasPrefix(args[1], "auditor.") || strings.HasPrefix(args[1], "judge.") {
			fmt.Println(dim("Model calls send redacted session excerpts to the configured provider. Restart the daemon to apply: credence stop && credence start"))
		}
		return nil
	}
	return flag2("usage: credence config [show | get key | set key value]")
}

func cmdBackfill(ctx context.Context, args []string) error {
	fs := newFlags("backfill", "[--since 30d] [--harness h]")
	since := fs.String("since", "30d", "how far back to capture")
	harness := fs.String("harness", "", "only this harness")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	dd, err := policy.ParseWindow(*since)
	if err != nil {
		return err
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	n, err := d.Ingest.Backfill(ctx, time.Now().Add(-dd), *harness)
	if err != nil {
		return err
	}
	// Old sessions are already over: close them and queue final reports.
	rows, _ := d.Store.ListSessions(store.SessionFilter{})
	queued := 0
	closeAfter := time.Duration(d.Cfg.Capture.CloseMinutes) * time.Minute
	for _, r := range rows {
		if r.ReportState != "" || r.EventCount == 0 || r.Skipped || time.Since(r.LastActivity) < closeAfter {
			continue
		}
		end := r.LastActivity
		_ = d.Store.SetSessionState(r.ID, "closed", &end)
		if ok, _ := d.Store.Enqueue("report", r.ID+":final:backfill", r.ID, "", time.Now()); ok {
			queued++
		}
	}
	fmt.Printf("captured %d transcripts from the last %s; queued %d reports for the daemon\n", n, *since, queued)
	return nil
}

// wouldDo words an install action for a dry run: "registered 6 hooks" is
// "would register 6 hooks".
func wouldDo(act string) string {
	if strings.HasPrefix(act, "would ") {
		return act
	}
	for _, v := range [][2]string{{"registered ", "would register "}, {"wrote ", "would write "}, {"added ", "would add "}, {"installed ", "would install "}, {"removed ", "would remove "}, {"updated ", "would update "}} {
		if strings.HasPrefix(act, v[0]) {
			return v[1] + strings.TrimPrefix(act, v[0])
		}
	}
	return "would: " + act
}
