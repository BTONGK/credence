package demo

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/claudecode"
	"github.com/BTONGK/credence/daemon/claims"
	"github.com/BTONGK/credence/daemon/ingest"
	"github.com/BTONGK/credence/daemon/store"
	"github.com/BTONGK/credence/daemon/view"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/keys"
	"github.com/BTONGK/credence/spec"
)

// Ingest captures the scenario's transcript and closes the session.
func Ingest(ctx context.Context, st *store.Store, cfg *config.Config, sc *Scenario, logger *log.Logger) (string, error) {
	a, ok := adapters.Get(claudecode.Name)
	if !ok {
		return "", fmt.Errorf("claude-code adapter not registered")
	}
	// The person talking to the agent is Sam, the bakery owner.
	c := *cfg
	c.Human = "Sam"
	eng := ingest.New(st, &c, logger, keys.RedactionKey(), time.Time{})
	if err := eng.ProcessFile(ctx, sc.Transcript, a, true); err != nil {
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

// Claims are the claims from the agent's replies, in the order it made them.
func Claims(st *store.Store, rep *view.Report) []*view.ClaimCard {
	var out []*view.ClaimCard
	for _, sec := range rep.Sections {
		for _, c := range sec.Claims {
			if c.Claim.Origin == spec.OriginStated {
				out = append(out, c)
			}
		}
	}
	// Claims from one reply follow the reply's own order.
	pos := func(c *view.ClaimCard) int { return -1 }
	if d, err := claims.Load(st, rep.Session.ID); err == nil {
		pos = func(c *view.ClaimCard) int {
			if e := d.Event(c.Claim.SourceEvent); e != nil {
				return strings.Index(claims.PayloadStr(e, "text"), c.Claim.Statement)
			}
			return -1
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Claim, out[j].Claim
		if a.SourceEvent != b.SourceEvent {
			return a.SourceEvent < b.SourceEvent
		}
		return pos(out[i]) < pos(out[j])
	})
	return out
}
