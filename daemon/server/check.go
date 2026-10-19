package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/quick"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/policy"
)

// CheckRequest asks for a quick check of a reply or a whole session. The
// Stop hook sends the harness session, its transcript and the reply text;
// the /credence skill sends the session alone.
type CheckRequest struct {
	Harness    string `json:"harness"`
	Session    string `json:"session"` // the harness's own session id
	Transcript string `json:"transcript"`
	Cwd        string `json:"cwd"`
	Reply      string `json:"reply"`
}

// check pulls the session's record forward and checks the claims in it.
func (d *Daemon) check(w http.ResponseWriter, r *http.Request) {
	var req CheckRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id, err := d.catchUp(ctx, req)
	if err != nil {
		notFound(w, err)
		return
	}
	res, err := quick.Check(ctx, d.Store, d.Cfg, id, strings.TrimSpace(req.Reply))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, res)
}

// catchUp ingests what the harness has written so far and returns the
// Credence session id.
func (d *Daemon) catchUp(ctx context.Context, req CheckRequest) (string, error) {
	d.Ingest.ConsumeSpool(ctx)
	var a adapters.Adapter
	if x, ok := adapters.Get(req.Harness); ok {
		a = x
	}
	var id string
	if req.Session != "" && req.Harness != "" {
		id = ingest.SessionID(req.Harness, req.Session)
	}
	path := req.Transcript
	if path == "" && id != "" {
		if s, err := d.Store.GetSession(id); err == nil {
			path = s.Transcript.Path
		}
	}
	if path == "" && a != nil && req.Session != "" {
		path = findTranscript(ctx, a, req.Session)
	}
	if path != "" {
		owner := a
		if pa, ok := adapters.ForPath(path); ok {
			owner = pa
		}
		if owner != nil {
			if err := d.Ingest.ProcessFile(ctx, path, owner, true); err != nil && !os.IsNotExist(err) {
				d.Log.Printf("check ingest %s: %v", path, err)
			}
		}
	}
	if id != "" {
		if _, err := d.Store.GetSession(id); err == nil {
			return id, nil
		}
	}
	// Without a session id, the newest session in this folder is the one.
	rows, err := d.Store.ListSessions(store.SessionFilter{Harness: req.Harness, Limit: 50})
	if err == nil {
		for _, s := range rows {
			if req.Cwd == "" || s.Cwd == req.Cwd || adapters.UnderDir(req.Cwd, s.Cwd) {
				return s.ID, nil
			}
		}
	}
	return "", errors.New("Credence has this session on its way in; ask again after the next reply")
}

// findTranscript looks under the harness's transcript folders for the file
// named after a session.
func findTranscript(ctx context.Context, a adapters.Adapter, session string) string {
	var found string
	for _, root := range a.TranscriptRoots() {
		base := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil || ctx.Err() != nil || found != "" {
				return fs.SkipDir
			}
			if de.IsDir() {
				if strings.Count(p, string(os.PathSeparator))-base > 5 {
					return fs.SkipDir
				}
				return nil
			}
			if strings.Contains(de.Name(), session) && a.IsTranscript(p) {
				found = p
				return fs.SkipAll
			}
			return nil
		})
		if found != "" {
			return found
		}
	}
	return ""
}

// BackfillResult says what a backfill brought in.
type BackfillResult struct {
	Transcripts int `json:"transcripts"`
	Queued      int `json:"queued"`
}

// Backfill captures transcripts modified since t, closes the sessions that
// are already over and queues their final reports.
func (d *Daemon) Backfill(ctx context.Context, since time.Time, harness string) (*BackfillResult, error) {
	n, err := d.Ingest.Backfill(ctx, since, harness)
	if err != nil {
		return nil, err
	}
	res := &BackfillResult{Transcripts: n}
	rows, _ := d.Store.ListSessions(store.SessionFilter{Since: since})
	closeAfter := time.Duration(d.Cfg.Capture.CloseMinutes) * time.Minute
	for _, s := range rows {
		if s.EventCount == 0 || s.Skipped || time.Since(s.LastActivity) < closeAfter {
			continue
		}
		if s.State == "closed" && s.ReportState != "none" && s.ReportState != "" {
			continue // reported already
		}
		if s.State != "closed" {
			end := s.LastActivity
			_ = d.Store.SetSessionState(s.ID, "closed", &end)
			d.Ingest.Forget(s.ID)
			s.State = "closed"
		}
		if d.enqueueReport(s, true) {
			res.Queued++
		}
	}
	return res, nil
}

func (d *Daemon) backfill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Since   string `json:"since"`
		Harness string `json:"harness"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Since == "" {
		req.Since = "7d"
	}
	dd, err := policy.ParseWindow(req.Since)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := d.Backfill(r.Context(), time.Now().Add(-dd), req.Harness)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, res)
}
