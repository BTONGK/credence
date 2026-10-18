// Package server is credenced: it consumes hook spools, tails transcripts,
// assembles sessions, runs report pipelines in the background, re-checks
// survival, and serves the local UI and API on 127.0.0.1.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/redact"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/survival"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/verifiers"
)

// Options configure the daemon.
type Options struct {
	Port    int       // 0 uses the configured port
	Log     io.Writer // nil logs to ~/.credence/logs/credenced.log
	NoScan  bool      // do not tail transcripts (tests, demo)
	Ready   func(*Info)
	Workers int
}

// Daemon is a running credenced.
type Daemon struct {
	Store    *store.Store
	Cfg      *config.Config
	Ingest   *ingest.Engine
	Pipeline *pipeline.Engine
	Ledger   *ledger.Ledger
	Signer   *keys.Signer
	Survival *survival.Worker
	Log      *log.Logger
	Info     *Info
	hub      *hub
	wake     chan struct{}
	started  time.Time
	opts     Options

	mu        sync.Mutex
	lastScan  time.Time
	lastFull  time.Time
	lastSurv  time.Time
	lastError string
}

// Open prepares a daemon without starting its loops (also used by the demo).
func Open(opts Options) (*Daemon, error) {
	cfg := config.Load()
	st, err := store.Open(home.DB())
	if err != nil {
		return nil, err
	}
	signer, err := keys.Load()
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("ledger key: %w", err)
	}
	out := opts.Log
	if out == nil {
		f, err := os.OpenFile(home.Path("logs", "credenced.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			out = f
		} else {
			out = os.Stderr
		}
	}
	lg := log.New(out, "", log.LstdFlags)
	d := &Daemon{Store: st, Cfg: cfg, Signer: signer, Log: lg, hub: newHub(), wake: make(chan struct{}, 1), started: time.Now(), opts: opts}
	d.Ledger = &ledger.Ledger{Store: st, Signer: signer}
	key := keys.RedactionKey()
	d.Ingest = ingest.New(st, cfg, lg, key, cfg.Capture.Since)
	d.Ingest.Notify = func(id string) { d.hub.publish("session", id) }
	d.Pipeline = &pipeline.Engine{Store: st, Cfg: cfg, Ledger: d.Ledger, Redactor: redact.New(key, nil), GitHub: verifiers.NewGitHub(), Log: lg,
		Changed: func(id string) { d.hub.publish("report", id) }}
	d.Pipeline.Plugins = loadPlugins(cfg, lg)
	d.Survival = &survival.Worker{Store: st, Ledger: d.Ledger}
	return d, nil
}

func loadPlugins(cfg *config.Config, lg *log.Logger) []*verifiers.Plugin {
	var out []*verifiers.Plugin
	for _, p := range cfg.Plugins {
		if len(p.Command) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pl, err := verifiers.LoadPlugin(ctx, p.Name, p.Command)
		cancel()
		if err != nil {
			lg.Printf("plugin %s: %v", p.Name, err)
			continue
		}
		out = append(out, pl)
	}
	return out
}

// Close releases resources.
func (d *Daemon) Close() { d.Store.Close() }

// Run starts the daemon and blocks until ctx is done.
func Run(ctx context.Context, opts Options) error {
	lk, err := lock()
	if err != nil {
		return err
	}
	defer lk.Close()
	d, err := Open(opts)
	if err != nil {
		return err
	}
	defer d.Close()
	_ = d.Store.ResetRunningJobs()
	port := opts.Port
	if port == 0 {
		port = d.Cfg.Port
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Fall back to any free port; daemon.json tells clients where we are.
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	d.Info = &Info{PID: os.Getpid(), Port: ln.Addr().(*net.TCPAddr).Port, Token: newToken(), Version: spec.Version, StartedAt: time.Now().UTC(), Home: home.Dir()}
	if err := writeInfo(d.Info); err != nil {
		return err
	}
	defer os.Remove(home.DaemonInfo())
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.Log.Printf("http: %v", err)
		}
	}()
	d.Log.Printf("credenced %s listening on %s (home %s)", spec.Version, d.Info.URL(), home.Dir())
	if opts.Ready != nil {
		opts.Ready(d.Info)
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = max(1, d.Cfg.Workers)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); d.worker(ctx) }()
	}
	d.loop(ctx)
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
	wg.Wait()
	return nil
}

// loop is the capture and assembly loop.
func (d *Daemon) loop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		d.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) tick(ctx context.Context, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			d.Log.Printf("tick panic: %v", r)
		}
	}()
	d.Ingest.ConsumeSpool(ctx)
	if !d.opts.NoScan {
		if now.Sub(d.lastFull) > 2*time.Minute {
			d.Ingest.Scan(ctx, true)
			d.lastFull, d.lastScan = now, now
		} else if now.Sub(d.lastScan) > 6*time.Second {
			d.Ingest.Scan(ctx, false)
			d.lastScan = now
		}
	}
	for _, id := range d.Ingest.DueClosures(now) {
		d.close(ctx, id, now)
	}
	d.assemble(ctx, now)
	if now.Sub(d.lastSurv) > time.Hour {
		d.lastSurv = now
		if _, err := d.Store.Enqueue("survival", now.UTC().Format("2006-01-02T15"), "", "", now); err == nil {
			d.poke()
		}
	}
}

// assemble decides which sessions need a provisional or final report.
func (d *Daemon) assemble(ctx context.Context, now time.Time) {
	rows, err := d.Store.ListSessions(store.SessionFilter{Since: now.Add(-72 * time.Hour)})
	if err != nil {
		return
	}
	idle := time.Duration(d.Cfg.Capture.IdleSeconds) * time.Second
	closeAfter := time.Duration(d.Cfg.Capture.CloseMinutes) * time.Minute
	for _, s := range rows {
		if s.EventCount == 0 || (s.Skipped && s.SkipReason == "internal") || s.SkipReason == "not_in_git_repo" || s.SkipReason == "excluded_repo" {
			continue
		}
		if s.State == "closed" {
			d.enqueueReport(s, true)
			continue
		}
		quiet := now.Sub(s.LastActivity)
		switch {
		case quiet >= closeAfter:
			d.close(ctx, s.ID, now)
		case quiet >= idle && (s.ReportAt.Before(s.LastActivity) || s.ReportAt.Before(s.StopSeenAt)):
			d.enqueueReport(s, false)
		}
	}
}

func (d *Daemon) enqueueReport(s *store.SessionRow, final bool) {
	state := "p"
	if final {
		state = "final"
	}
	head := s.HeadHash
	if len(head) > 19 {
		head = head[7:19]
	}
	if ok, _ := d.Store.Enqueue("report", s.ID+":"+state+":"+head, s.ID, "", time.Now()); ok {
		d.poke()
	}
}

// close finalizes a session after an integrity check of its transcript.
func (d *Daemon) close(ctx context.Context, id string, now time.Time) {
	s, err := d.Store.GetSession(id)
	if err != nil || s.State == "closed" {
		return
	}
	d.Ingest.RefreshSession(ctx, id)
	if s.Transcript.Path != "" {
		if bad, err := d.Ingest.VerifyTranscript(s.Transcript.Path); err == nil && len(bad) > 0 {
			_ = d.Store.AddSessionFlag(id, "transcript_rewritten")
			d.Log.Printf("session %s: %d transcript lines changed after capture", id, len(bad))
		}
	}
	s, _ = d.Store.GetSession(id)
	end := s.LastActivity
	_ = d.Store.SetSessionState(id, "closed", &end)
	d.Ingest.Forget(id)
	s.State = "closed"
	d.enqueueReport(s, true)
	d.hub.publish("session", id)
}

func (d *Daemon) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// worker runs background jobs.
func (d *Daemon) worker(ctx context.Context) {
	for ctx.Err() == nil {
		j, err := d.Store.NextJob([]string{"report", "verify", "survival"})
		if err != nil || j == nil {
			select {
			case <-ctx.Done():
				return
			case <-d.wake:
			case <-time.After(3 * time.Second):
			}
			continue
		}
		jerr := d.runJob(ctx, j)
		if jerr != nil {
			d.Log.Printf("job %s %s: %v", j.Kind, j.Key, jerr)
		}
		_ = d.Store.FinishJob(j, jerr, 4)
	}
}

func (d *Daemon) runJob(ctx context.Context, j *store.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	switch j.Kind {
	case "report":
		final := strings.Contains(j.Key, ":final:")
		_, err = d.Pipeline.Report(ctx, j.SessionID, pipeline.Options{Final: final})
		if err != nil && strings.Contains(err.Error(), "is a subagent of") {
			return nil
		}
		return err
	case "verify":
		_, _, err = d.Pipeline.VerifyOne(ctx, j.ClaimID, strings.HasSuffix(j.Key, ":rerun"))
		return err
	case "survival":
		n, err := d.Survival.RunDue(ctx)
		if n > 0 {
			d.Log.Printf("survival: recorded %d checks", n)
			d.hub.publish("ledger", "")
		}
		return err
	}
	return fmt.Errorf("unknown job kind %s", j.Kind)
}

// hub fans out change notifications to SSE clients.
type hub struct {
	mu   sync.Mutex
	subs map[chan string]bool
}

func newHub() *hub { return &hub{subs: map[chan string]bool{}} }

func (h *hub) subscribe() chan string {
	c := make(chan string, 32)
	h.mu.Lock()
	h.subs[c] = true
	h.mu.Unlock()
	return c
}

func (h *hub) unsubscribe(c chan string) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}

func (h *hub) publish(kind, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- kind + ":" + id:
		default:
		}
	}
}
