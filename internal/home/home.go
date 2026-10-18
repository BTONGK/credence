// Package home locates Credence's local state directory (~/.credence, or
// $CREDENCE_HOME) and the files inside it.
package home

import (
	"os"
	"path/filepath"
)

// Dir returns the Credence state directory, creating it if needed.
func Dir() string {
	d := os.Getenv("CREDENCE_HOME")
	if d == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			h = os.TempDir()
		}
		d = filepath.Join(h, ".credence")
	}
	_ = os.MkdirAll(d, 0o700)
	return d
}

// Path joins elements under the state directory.
func Path(elem ...string) string {
	return filepath.Join(append([]string{Dir()}, elem...)...)
}

// Sub returns a subdirectory of the state directory, creating it.
func Sub(name string) string {
	p := Path(name)
	_ = os.MkdirAll(p, 0o700)
	return p
}

// DB is the SQLite database path.
func DB() string { return Path("credence.db") }

// Spool is the directory hooks append to.
func Spool() string { return Sub("spool") }

// Keys holds the ledger signing key.
func Keys() string { return Sub("keys") }

// Worktrees holds clean checkouts used by verifiers.
func Worktrees() string { return Sub("worktrees") }

// Logs holds daemon logs.
func Logs() string { return Sub("logs") }

// DaemonInfo is the file the daemon writes its address and API token to.
func DaemonInfo() string { return Path("daemon.json") }

// Config is the user-level configuration file.
func Config() string { return Path("config.yaml") }

// UserHome returns the user's home directory.
func UserHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return h
}
