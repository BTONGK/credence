package main

import (
	"context"
	"fmt"
	"time"

	"github.com/BTONGK/credence/importers"
)

func cmdImport(ctx context.Context, args []string) error {
	fs := newFlags("import", "agent-trace|git-ai|entire|otlp|transcript <path> [--repo dir] [--harness name]")
	repo := fs.String("repo", ".", "repository the data belongs to")
	harness := fs.String("harness", "", "harness name for imported transcripts")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		fs.Usage()
		return flag2("expected an import kind")
	}
	path := ""
	if len(pos) > 1 {
		path = pos[1]
	}
	d, err := open()
	if err != nil {
		return err
	}
	defer d.Close()
	res, err := importers.Run(ctx, importers.Options{Kind: pos[0], Path: path, Repo: *repo, Harness: *harness, Store: d.Store, Ingest: d.Ingest})
	if err != nil {
		return err
	}
	// Imported sessions are usually over: close them and queue final reports.
	queued := 0
	closeAfter := time.Duration(d.Cfg.Capture.CloseMinutes) * time.Minute
	for _, sid := range res.Sessions {
		r, err := d.Store.GetSession(sid)
		if err != nil || r.EventCount == 0 || time.Since(r.LastActivity) < closeAfter {
			continue
		}
		end := r.LastActivity
		_ = d.Store.SetSessionState(sid, "closed", &end)
		if ok, _ := d.Store.Enqueue("report", sid+":final:import", sid, "", time.Now()); ok {
			queued++
		}
	}
	fmt.Println(res)
	if queued > 0 {
		fmt.Printf("queued %d reports for the daemon\n", queued)
	}
	return nil
}
