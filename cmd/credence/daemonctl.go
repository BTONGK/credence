package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/BTONGK/credence/daemon/server"
	"github.com/BTONGK/credence/internal/config"
	"github.com/BTONGK/credence/internal/home"
)

// open returns a daemon-shaped engine over the local store for commands
// that work without the background daemon.
func open() (*server.Daemon, error) {
	return server.Open(server.Options{Log: io.Discard})
}

func cmdDaemon(ctx context.Context, args []string) error {
	fs := newFlags("daemon", "[--port n] [--no-scan]")
	port := fs.Int("port", 0, "listen port (default from config, 7171)")
	verbose := fs.Bool("v", false, "log to stderr")
	noScan := fs.Bool("no-scan", false, "serve and report imported sessions only, leaving harness transcripts alone")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	opts := server.Options{Port: *port, NoScan: *noScan}
	if *verbose {
		opts.Log = os.Stderr
	}
	opts.Ready = func(i *server.Info) {
		fmt.Fprintf(os.Stderr, "credenced listening on %s (pid %d)\n", i.URL(), i.PID)
	}
	return server.Run(ctx, opts)
}

// startBackground launches the daemon detached and waits for it to answer.
func startBackground() (*server.Info, error) {
	if i, err := server.ReadInfo(); err == nil && i.Ping() == nil {
		return i, nil
	}
	logf, _ := os.OpenFile(home.Path("logs", "credenced.out"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	cmd := exec.Command(exe(), "daemon")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		if i, err := server.ReadInfo(); err == nil && i.Ping() == nil {
			return i, nil
		}
	}
	return nil, fmt.Errorf("daemon did not start; see %s", home.Path("logs", "credenced.out"))
}

func cmdStart(ctx context.Context, args []string) error {
	i, err := startBackground()
	if err != nil {
		return err
	}
	fmt.Printf("credenced running on %s (pid %d)\n", i.URL(), i.PID)
	return nil
}

func cmdStop(ctx context.Context, args []string) error {
	i, err := server.ReadInfo()
	if err != nil {
		fmt.Println("credenced is not running")
		return nil
	}
	if err := stopDaemon(i); err != nil {
		return err
	}
	fmt.Printf("stopped credenced (pid %d)\n", i.PID)
	return nil
}

// stopDaemon asks the daemon to exit and waits up to three seconds.
func stopDaemon(i *server.Info) error {
	if err := syscall.Kill(i.PID, syscall.SIGTERM); err != nil {
		return err
	}
	for k := 0; k < 30; k++ {
		time.Sleep(100 * time.Millisecond)
		if syscall.Kill(i.PID, 0) != nil {
			break
		}
	}
	return nil
}

// autostart starts the daemon from the capture hook when it is not
// running. It is rate-limited and never blocks the hook.
func autostart() {
	defer func() { _ = recover() }()
	if os.Getenv("CREDENCE_INTERNAL") != "" || os.Getenv("CREDENCE_NO_AUTOSTART") != "" {
		return
	}
	if i, err := server.ReadInfo(); err == nil && i.PID > 0 {
		return
	}
	stamp := home.Path("autostart.stamp")
	if fi, err := os.Stat(stamp); err == nil && time.Since(fi.ModTime()) < time.Minute {
		return
	}
	cfg := config.Load()
	if cfg.Autostart != nil && !*cfg.Autostart {
		return
	}
	_ = os.WriteFile(stamp, nil, 0o600)
	cmd := exec.Command(exe(), "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if f, err := os.OpenFile(home.Path("logs", "credenced.out"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
	}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

func openBrowser(url string) error {
	if os.Getenv("CREDENCE_NO_OPEN") != "" {
		return errors.New("CREDENCE_NO_OPEN is set")
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}

func cmdUI(ctx context.Context, args []string) error {
	fs := newFlags("ui", "[--no-open]")
	noOpen := fs.Bool("no-open", false, "print the URL instead of opening a browser")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	i, err := startBackground()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/#token=%s", i.URL(), i.Token)
	if *noOpen || os.Getenv("CREDENCE_NO_OPEN") != "" {
		fmt.Println(url)
		return nil
	}
	fmt.Printf("Opening %s\n", i.URL())
	if err := openBrowser(url); err != nil {
		fmt.Println(url)
	}
	return nil
}
