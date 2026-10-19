package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BTONGK/credence/daemon/calibration"
	"github.com/BTONGK/credence/daemon/ledger"
	"github.com/BTONGK/credence/daemon/pipeline"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/gitx"
	"github.com/BTONGK/credence/internal/home"
	"github.com/BTONGK/credence/policy"
	"github.com/BTONGK/credence/spec"
	"github.com/BTONGK/credence/ui"
)

// Handler is the daemon's HTTP surface: the embedded UI and /api.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()
	api.HandleFunc("GET /api/status", d.status)
	api.HandleFunc("GET /api/sessions", d.sessions)
	api.HandleFunc("GET /api/sessions/{id}", d.session)
	api.HandleFunc("GET /api/sessions/{id}/events", d.events)
	api.HandleFunc("POST /api/sessions/{id}/report", d.rereport)
	api.HandleFunc("POST /api/sessions/{id}/claims", d.addClaim)
	api.HandleFunc("POST /api/sessions/{id}/review_timing", d.reviewTiming)
	api.HandleFunc("GET /api/claims/{id}", d.claim)
	api.HandleFunc("POST /api/claims/{id}/review", d.review)
	api.HandleFunc("POST /api/claims/{id}/verify", d.verify)
	api.HandleFunc("PUT /api/claims/{id}/attribution", d.setAttribution)
	api.HandleFunc("GET /api/test_runs/{id}", d.testRun)
	api.HandleFunc("GET /api/diff", d.diff)
	api.HandleFunc("GET /api/file", d.file)
	api.HandleFunc("GET /api/ledger", d.reputation)
	api.HandleFunc("GET /api/ledger/entries", d.ledgerEntries)
	api.HandleFunc("GET /api/ledger/verify", d.ledgerVerify)
	api.HandleFunc("GET /api/export", d.export)
	api.HandleFunc("GET /api/stream", d.stream)
	api.HandleFunc("GET /api/summary", d.summary)
	api.HandleFunc("POST /api/check", d.check)
	api.HandleFunc("POST /api/backfill", d.backfill)
	mux.Handle("/api/", d.guard(api))
	mux.Handle("POST /v1/logs", d.hostCheck(http.HandlerFunc(d.otlp)))
	mux.Handle("POST /v1/traces", d.hostCheck(http.HandlerFunc(d.otlp)))
	static := http.FileServerFS(ui.FS())
	mux.Handle("/", d.hostCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != "/" {
			if _, err := fs.Stat(ui.FS(), strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				r.URL.Path = "/" // SPA route
			}
		}
		static.ServeHTTP(w, r)
	})))
	return mux
}

// hostCheck defeats DNS rebinding: only loopback host names are served.
func (d *Daemon) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch host {
		case "127.0.0.1", "localhost", "::1", "[::1]":
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guard requires the daemon token and a same-origin request.
func (d *Daemon) guard(next http.Handler) http.Handler {
	return d.hostCheck(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.Header.Get("X-Credence-Token")
		}
		if tok == "" && r.URL.Path == "/api/stream" {
			tok = r.URL.Query().Get("token") // EventSource cannot set headers
		}
		if d.Info == nil || subtle.ConstantTimeCompare([]byte(tok), []byte(d.Info.Token)) != 1 {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or wrong token; open the UI with `credence ui`"))
			return
		}
		if o := r.Header.Get("Origin"); o != "" && r.Method != "GET" {
			if !strings.HasPrefix(o, "http://127.0.0.1:") && !strings.HasPrefix(o, "http://localhost:") {
				writeErr(w, http.StatusForbidden, errors.New("cross-origin request refused"))
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	}))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func notFound(w http.ResponseWriter, err error) { writeErr(w, http.StatusNotFound, err) }

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func (d *Daemon) status(w http.ResponseWriter, r *http.Request) {
	hooks, _ := d.Store.HookStats(time.Now().Add(-24 * time.Hour))
	writeJSON(w, map[string]any{
		"version": spec.Version, "pid": d.Info.PID, "port": d.Info.Port, "home": home.Dir(), "started_at": d.Info.StartedAt,
		"jobs": d.Store.JobCounts(), "job_errors": d.Store.RecentJobErrors(5), "hooks_24h": hooks,
		"month_spend_usd": d.Store.MonthSpend(), "budget_usd": d.Cfg.Budget.MonthlyUSD,
		"auditor": d.Cfg.Auditor.Provider, "judge": d.Cfg.Judge.Provider, "human": d.Cfg.Human, "person_views": d.Cfg.PersonViews,
	})
}

// summary sums up the agents' work over a period: ?range=7d, 30d or all.
func (d *Daemon) summary(w http.ResponseWriter, r *http.Request) {
	var since time.Time
	if rg := r.URL.Query().Get("range"); rg != "all" {
		if rg == "" {
			rg = "7d"
		}
		dd, err := policy.ParseWindow(rg)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		since = time.Now().Add(-dd)
	}
	sum, err := view.Summarize(d.Store, since)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, sum)
}

func (d *Daemon) sessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SessionFilter{NeedsReview: q.Get("needs_review") == "1", Repo: q.Get("repo"), Harness: q.Get("harness"),
		AgentConfig: q.Get("agent_config"), Query: q.Get("q"), Limit: 200}
	if s := q.Get("since"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			f.Since = t
		}
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		f.Limit = n
	}
	items, err := view.Inbox(d.Store, f)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"sessions": items})
}

func (d *Daemon) session(w http.ResponseWriter, r *http.Request) {
	s, err := d.Store.FindSession(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	rep, err := view.BuildReport(d.Store, s.ID)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, rep)
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	to, _ := strconv.Atoi(r.URL.Query().Get("to"))
	if to > 0 && to-from > 2000 {
		to = from + 2000
	}
	evs, err := d.Store.Events(id, from, to)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if evs == nil {
		evs = []spec.Event{}
	}
	writeJSON(w, map[string]any{"events": evs})
}

func (d *Daemon) rereport(w http.ResponseWriter, r *http.Request) {
	s, err := d.Store.FindSession(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	sum, err := d.Pipeline.Report(ctx, s.ID, pipeline.Options{Final: s.State == "closed", Reverify: true})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, sum)
}

func (d *Daemon) human(sessionID string) string {
	if d.Cfg.Human != "" {
		return "human:" + strings.ToLower(d.Cfg.Human)
	}
	if s, err := d.Store.GetSession(sessionID); err == nil && s.Human != "" {
		return s.Human
	}
	return "human:local"
}

func (d *Daemon) claim(w http.ResponseWriter, r *http.Request) {
	row, err := d.Store.GetClaim(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	entries, _ := d.Store.LedgerEntries(store.LedgerFilter{ClaimID: row.Claim.ID})
	if entries == nil {
		entries = []spec.LedgerEntry{}
	}
	writeJSON(w, map[string]any{"card": view.Card(d.Store, row), "ledger": entries})
}

type reviewReq struct {
	Decision        string `json:"decision"`
	Reason          string `json:"reason"`
	Note            string `json:"note"`
	EditedTitle     string `json:"edited_title"`
	EditedStatement string `json:"edited_statement"`
}

func (d *Daemon) review(w http.ResponseWriter, r *http.Request) {
	var req reviewReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	row, err := d.Store.GetClaim(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	e, err := d.Ledger.Decide(r.Context(), row.Claim.ID, spec.HumanDecision{Decision: req.Decision, By: d.human(row.Claim.SessionID),
		Reason: req.Reason, Note: req.Note, EditedTitle: req.EditedTitle, EditedStatement: req.EditedStatement})
	if err != nil {
		code := 400
		if errors.Is(err, ledger.ErrContradicted) {
			code = 409
		}
		writeErr(w, code, err)
		return
	}
	d.hub.publish("report", row.Claim.SessionID)
	updated, _ := d.Store.GetClaim(row.Claim.ID)
	writeJSON(w, map[string]any{"entry": e, "card": view.Card(d.Store, updated)})
}

func (d *Daemon) verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rerun bool `json:"rerun"`
		Wait  bool `json:"wait"`
	}
	_ = decode(r, &req)
	row, err := d.Store.GetClaim(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	if !req.Wait {
		key := row.Claim.ID + ":" + time.Now().UTC().Format(time.RFC3339Nano)
		if req.Rerun {
			key += ":rerun"
		}
		_, _ = d.Store.Enqueue("verify", key, row.Claim.SessionID, row.Claim.ID, time.Now())
		d.poke()
		writeJSON(w, map[string]any{"queued": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	vs, dec, err := d.Pipeline.VerifyOne(ctx, row.Claim.ID, req.Rerun)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, map[string]any{"verdicts": vs, "decision": dec})
}

func (d *Daemon) setAttribution(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Shares []spec.Share `json:"shares"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	row, err := d.Store.GetClaim(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	sum := 0.0
	for i, s := range req.Shares {
		if s.Contributor == "" || s.Share < 0 {
			writeErr(w, 400, errors.New("each share needs a contributor and a non-negative share"))
			return
		}
		if !strings.Contains(s.Contributor, ":") {
			req.Shares[i].Contributor = "human:" + strings.ToLower(s.Contributor)
		}
		sum += s.Share
	}
	if len(req.Shares) == 0 || sum < 0.99 || sum > 1.01 {
		writeErr(w, 400, fmt.Errorf("shares must add up to 1 (got %.2f)", sum))
		return
	}
	a := &spec.Attribution{ClaimID: row.Claim.ID, Shares: req.Shares, Method: "human_override:" + d.human(row.Claim.SessionID)}
	if err := d.Store.PutAttribution(a, true); err != nil {
		writeErr(w, 500, err)
		return
	}
	d.hub.publish("report", row.Claim.SessionID)
	writeJSON(w, a)
}

func (d *Daemon) addClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type       string           `json:"type"`
		Title      string           `json:"title"`
		Statement  string           `json:"statement"`
		Evidence   []string         `json:"evidence"`
		Assertions []spec.Assertion `json:"assertions"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	s, err := d.Store.FindSession(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	if !spec.IsClaimType(req.Type) || strings.TrimSpace(req.Title) == "" {
		writeErr(w, 400, errors.New("a claim needs a valid type and a title"))
		return
	}
	c := &spec.Claim{SessionID: s.ID, Claimant: d.human(s.ID), Origin: spec.OriginHuman, Type: req.Type, Title: req.Title,
		Statement: req.Statement, Assertions: req.Assertions, Evidence: []spec.Evidence{}, CreatedAt: time.Now().UTC()}
	if c.Assertions == nil {
		c.Assertions = []spec.Assertion{}
	}
	if c.Statement == "" {
		c.Statement = c.Title
	}
	for _, t := range req.Evidence {
		c.Evidence = append(c.Evidence, spec.Evidence{Target: t, Relation: spec.RelProduced})
	}
	c.AssignID()
	if _, err := d.Store.InsertClaim(c); err != nil {
		writeErr(w, 500, err)
		return
	}
	d.enqueueReport(s, s.State == "closed")
	writeJSON(w, c)
}

func (d *Daemon) reviewTiming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ms     int64 `json:"ms"`
		Claims int   `json:"claims"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	_ = d.Store.RecordReviewTiming(r.PathValue("id"), req.Ms, req.Claims, "ui")
	writeJSON(w, map[string]bool{"ok": true})
}

func (d *Daemon) testRun(w http.ResponseWriter, r *http.Request) {
	tr, err := d.Store.GetTestRun(r.PathValue("id"))
	if err != nil {
		notFound(w, err)
		return
	}
	writeJSON(w, tr)
}

func (d *Daemon) repoOf(sessionID string) (string, error) {
	s, err := d.Store.FindSession(sessionID)
	if err != nil {
		return "", err
	}
	if s.Repo == "" {
		return "", errors.New("session has no repository")
	}
	return s.Repo, nil
}

func (d *Daemon) diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, err := d.repoOf(q.Get("session"))
	if err != nil {
		notFound(w, err)
		return
	}
	commit := q.Get("commit")
	if _, err := gitx.ResolveCommit(repo, commit); err != nil {
		notFound(w, err)
		return
	}
	var paths []string
	if p := q.Get("path"); p != "" {
		paths = append(paths, p)
	}
	out, err := gitx.Diff(repo, commit, paths...)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if len(out) > 400000 {
		out = out[:400000] + "\n…[diff truncated]"
	}
	cm, _ := gitx.ShowCommit(repo, commit)
	writeJSON(w, map[string]any{"commit": cm, "diff": d.Pipeline.Redactor.String(out)})
}

func (d *Daemon) file(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, err := d.repoOf(q.Get("session"))
	if err != nil {
		notFound(w, err)
		return
	}
	commit := q.Get("commit")
	if commit == "" {
		commit = "HEAD"
	}
	content, err := gitx.FileAt(repo, commit, q.Get("path"))
	if err != nil {
		notFound(w, err)
		return
	}
	if len(content) > 400000 {
		content = content[:400000]
	}
	writeJSON(w, map[string]any{"path": q.Get("path"), "commit": commit, "content": d.Pipeline.Redactor.String(content)})
}

func (d *Daemon) reputation(w http.ResponseWriter, r *http.Request) {
	by := calibration.GroupBy(r.URL.Query().Get("by"))
	if by == "" {
		by = calibration.ByAgent
	}
	stats, err := calibration.Compute(d.Store, by, time.Time{})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if stats == nil {
		stats = []*calibration.Stat{}
	}
	cfgs, _ := d.Store.AgentConfigs()
	writeJSON(w, map[string]any{"by": by, "stats": stats, "agent_configs": cfgs})
}

func (d *Daemon) ledgerEntries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	es, err := d.Store.LedgerEntries(store.LedgerFilter{ClaimID: r.URL.Query().Get("claim")})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if limit > 0 && len(es) > limit {
		es = es[len(es)-limit:]
	}
	if es == nil {
		es = []spec.LedgerEntry{}
	}
	writeJSON(w, map[string]any{"entries": es})
}

func (d *Daemon) ledgerVerify(w http.ResponseWriter, r *http.Request) {
	es, err := d.Store.LedgerEntries(store.LedgerFilter{})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, ledger.VerifyChain(es, d.Signer.Pub))
}

func (d *Daemon) export(w http.ResponseWriter, r *http.Request) {
	es, err := d.Store.LedgerEntries(store.LedgerFilter{})
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	var buf bytes.Buffer
	switch r.URL.Query().Get("format") {
	case "csv":
		err = ledger.WriteCSV(&buf, es, func(id string) float64 {
			if c, err := d.Store.GetClaim(id); err == nil {
				return c.Value
			}
			return 0
		})
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="credence-ledger.csv"`)
	default:
		err = ledger.WriteJSON(&buf, es, d.Signer.Pub)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="credence-ledger.json"`)
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func (d *Daemon) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	c := d.hub.subscribe()
	defer d.hub.unsubscribe(c)
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-c:
			kind, id, _ := strings.Cut(msg, ":")
			b, _ := json.Marshal(map[string]string{"kind": kind, "id": id})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
