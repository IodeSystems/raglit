package main

import (
	"os"
	"testing"
)

func TestDaemonStateRoundtrip(t *testing.T) {
	root := t.TempDir()
	if _, ok := readDaemonState(root); ok {
		t.Fatal("no state should exist yet")
	}
	remove, err := writeDaemonState(root, "127.0.0.1:7461")
	if err != nil {
		t.Fatalf("writeDaemonState: %v", err)
	}
	st, ok := readDaemonState(root)
	if !ok {
		t.Fatal("state should be readable after write")
	}
	if st.Addr != "127.0.0.1:7461" {
		t.Errorf("addr = %q, want 127.0.0.1:7461", st.Addr)
	}
	if st.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", st.PID, os.Getpid())
	}
	remove()
	if _, ok := readDaemonState(root); ok {
		t.Fatal("state should be gone after remove()")
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("current process must be alive")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Error("non-positive pids are never alive")
	}
	// PID 2^31-1 is effectively never a live process.
	if pidAlive(2147483646) {
		t.Error("an absurd pid should not be alive")
	}
}

// stopDaemon on a stale state (pid not running) removes the file and errors.
func TestStopDaemonStale(t *testing.T) {
	root := t.TempDir()
	if _, err := writeDaemonState(root, "127.0.0.1:7461"); err != nil {
		t.Fatal(err)
	}
	// Rewrite with a dead pid.
	if err := os.WriteFile(daemonStatePath(root),
		[]byte(`{"pid":2147483646,"addr":"127.0.0.1:7461"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stopDaemon(root); err == nil {
		t.Error("stopping a dead daemon should error")
	}
	if _, ok := readDaemonState(root); ok {
		t.Error("stale state should be removed")
	}
}

func TestStopDaemonMissing(t *testing.T) {
	if err := stopDaemon(t.TempDir()); err == nil {
		t.Error("stopping with no state file should error")
	}
}
