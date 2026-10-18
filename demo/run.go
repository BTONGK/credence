package demo

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/claudecode"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/keys"
)

// Ingest captures the scenario's transcripts and closes the session.
func Ingest(ctx context.Context, st *store.Store, cfg *config.Config, sc *Scenario, logger *log.Logger) (string, error) {
	a, ok := adapters.Get(claudecode.Name)
	if !ok {
		return "", fmt.Errorf("claude-code adapter not registered")
	}
	eng := ingest.New(st, cfg, logger, keys.RedactionKey(), time.Time{})
	if err := eng.ProcessFile(ctx, sc.Transcript, a, true); err != nil {
		return "", err
	}
	if err := eng.ProcessFile(ctx, sc.Subagent, a, true); err != nil {
		return "", err
	}
	sid := ingest.SessionID(claudecode.Name, sc.SessionID)
	s, err := st.GetSession(sid)
	if err != nil {
		return "", fmt.Errorf("demo session was not captured: %w", err)
	}
	end := s.LastActivity
	if err := st.SetSessionState(sid, "closed", &end); err != nil {
		return "", err
	}
	return sid, nil
}
