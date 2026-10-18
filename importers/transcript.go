package importers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BTONGK/credence/adapters"
	"github.com/BTONGK/credence/adapters/generic"
)

// Transcript imports a transcript file. With --harness naming a dedicated
// adapter (claude-code, codex, cursor) it is parsed natively in place;
// otherwise it is copied into the generic drop directory with the working
// directory filled in and parsed as chat-style JSONL.
func Transcript(ctx context.Context, o Options) (*Result, error) {
	r := &Result{Kind: "transcript", Records: 1}
	if o.Path == "" {
		return nil, errors.New("transcript: expected a file")
	}
	abs, err := filepath.Abs(o.Path)
	if err != nil {
		return nil, err
	}
	name := o.Harness
	if name == "" {
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		name = adapterForAgent("", b)
	}
	a, ok := adapters.Get(name)
	if !ok {
		return nil, errors.New("transcript: unknown harness " + name)
	}
	file := abs
	if name == generic.Name {
		b, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		dir := filepath.Join(generic.Root(), "imported")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		file = filepath.Join(dir, safeName(base)+".jsonl")
		if err := os.WriteFile(file, withCwd(b, o.Repo, true), 0o600); err != nil {
			return nil, err
		}
	}
	sids, err := ingestFile(ctx, o, file, a)
	if err != nil {
		return nil, err
	}
	r.Sessions = sids
	return r, nil
}
