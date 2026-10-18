package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/BTONGK/credence/internal/home"
)

// Info is written to ~/.credence/daemon.json while the daemon runs.
type Info struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	Token     string    `json:"token"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Home      string    `json:"home"`
}

// URL is the daemon's base URL.
func (i *Info) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", i.Port) }

// ReadInfo returns the running daemon's info, or an error if none runs.
func ReadInfo() (*Info, error) {
	b, err := os.ReadFile(home.DaemonInfo())
	if err != nil {
		return nil, errors.New("daemon is not running")
	}
	var i Info
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, err
	}
	if i.PID <= 0 || syscall.Kill(i.PID, 0) != nil {
		return nil, errors.New("daemon is not running (stale daemon.json)")
	}
	return &i, nil
}

// Ping checks that the daemon answers.
func (i *Info) Ping() error {
	req, _ := http.NewRequest("GET", i.URL()+"/api/status", nil)
	req.Header.Set("Authorization", "Bearer "+i.Token)
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("daemon answered %s", resp.Status)
	}
	return nil
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeInfo(i *Info) error {
	b, _ := json.MarshalIndent(i, "", "  ")
	tmp := home.DaemonInfo() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, home.DaemonInfo())
}

// lock takes the single-instance lock; the returned file must stay open.
func lock() (*os.File, error) {
	f, err := os.OpenFile(home.Path("daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another credence daemon is already running")
	}
	return f, nil
}
