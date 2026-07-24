package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Daemon runtime state (<root>/daemon.json).
//
// The daemon writes this on startup and removes it on clean shutdown. It lets a
// client DISCOVER a running daemon's real address — so a daemon on a non-default
// port is found instead of a second one being spawned on 7420 — and lets
// `raglit daemon --stop` signal it. The health probe is still authoritative for
// "is it up"; this file only records where + who.
type daemonState struct {
	PID       int    `json:"pid"`
	Addr      string `json:"addr"` // host:port it listens on
	Root      string `json:"root"` // storage root it owns
	StartedAt string `json:"started_at"`
	Version   string `json:"version"`
}

// daemonStatePath is <root>/daemon.json. Clients and the daemon agree on it
// because both resolve the same DefaultRoot() (env RAGLIT_ROOT else ~/.raglit)
// when no explicit root is given.
func daemonStatePath(root string) string { return filepath.Join(root, "daemon.json") }

// writeDaemonState records this process as the daemon owning root@addr, and
// returns a remover to call on clean shutdown.
func writeDaemonState(root, addr string) (func(), error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	st := daemonState{
		PID:       os.Getpid(),
		Addr:      addr,
		Root:      root,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Version:   version,
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	path := daemonStatePath(root)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

// readDaemonState loads <root>/daemon.json, if present.
func readDaemonState(root string) (*daemonState, bool) {
	b, err := os.ReadFile(daemonStatePath(root))
	if err != nil {
		return nil, false
	}
	var st daemonState
	if json.Unmarshal(b, &st) != nil || st.Addr == "" {
		return nil, false
	}
	return &st, true
}

// pidAlive reports whether the process is running. signal 0 probes existence:
// nil = alive, EPERM = alive but not ours, ESRCH = gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// stopDaemon signals the recorded daemon to shut down (SIGTERM) and reports it.
// Used by `raglit daemon --stop`.
func stopDaemon(root string) error {
	st, ok := readDaemonState(root)
	if !ok {
		return fmt.Errorf("no daemon state at %s (none running under this root?)", daemonStatePath(root))
	}
	if !pidAlive(st.PID) {
		os.Remove(daemonStatePath(root))
		return fmt.Errorf("daemon pid %d not running — removed stale %s", st.PID, daemonStatePath(root))
	}
	if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal daemon pid %d: %w", st.PID, err)
	}
	fmt.Printf("stopped daemon pid %d (%s)\n", st.PID, st.Addr)
	return nil
}
