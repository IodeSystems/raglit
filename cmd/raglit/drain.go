package main

import (
	"fmt"
	"os"
	"time"
)

// The daemon's drain graces (raglit.Runner.Drain). The cut-off wait after the
// grace is the library's (raglit.CutOffWait).
const (
	// stopGrace is how long SIGTERM waits for running jobs. The systemd unit
	// gives the stop TimeoutStopSec=120 and kills after that: the grace, the
	// cut-off and the HTTP shutdown have to fit inside it.
	stopGrace = 90 * time.Second
	// reexecGrace is how long a re-exec (source rebuild, SIGHUP) waits. Nothing
	// outside kills the process here, so it can wait as long as dun waits
	// before an upgrade.
	reexecGrace = 10 * time.Minute
)

// daemonNotef prints the Runner's drain notices as the daemon always has.
func daemonNotef(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "raglit daemon: "+format+"\n", args...)
}
