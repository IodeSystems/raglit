package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/iodesystems/raglit/internal/update"
)

// daemonReleaseUpdate keeps a release-build daemon on the newest release.
//
// Once a day (update.Interval; the state is <root>/update.json) it fetches the
// signed manifest of the latest GitHub release, verifies it with the embedded
// key, and when it names a newer raglit for this platform, downloads and
// checks the archive into a staging file beside the binary. The staged binary
// must pass `daemon --check` with this daemon's own flags before it replaces
// the binary. Then the queue drains (raglit.Runner.Drain) and the daemon
// re-execs into the new build, with the same PID, as SIGHUP does.
//
// dun does the same check and then exits 75 for systemd to restart it, because
// dund hands sessions over between two processes. raglit has no state to hand
// over (MCP is stateless here, the queue is in SQLite), so a re-exec is enough
// and the unit needs no change.
//
// Off for every build but a release build, and with RAGLIT_NO_UPDATE=1.
func daemonReleaseUpdate(root string, drain func()) {
	if !releaseBuild || releaseManifestURL == "" || os.Getenv("RAGLIT_NO_UPDATE") == "1" {
		return
	}
	exe, err := selfPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit daemon: release updates off: %v\n", err)
		return
	}
	u, err := releaseUpdater(root, exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "raglit daemon: release updates off: %v\n", err)
		return
	}
	for {
		if to, err := stageRelease(u, exe, os.Args[1:]); err != nil {
			if !errors.Is(err, update.ErrNotDue) {
				fmt.Fprintf(os.Stderr, "raglit daemon: release update: %v\n", err)
			}
		} else if to != "" {
			fmt.Fprintf(os.Stderr, "raglit daemon: raglit %s installed — draining the queue, then restarting into it\n", to)
			drain()
			reexecFailed(syscall.Exec(exe, os.Args, os.Environ()))
		}
		// Run itself says when a check is due; this only wakes it.
		time.Sleep(time.Hour)
	}
}

// releaseUpdater is the check for this binary, staging into <exe>.new.
func releaseUpdater(root, exe string) (*update.Updater, error) {
	keys, err := update.ParseKeys(releaseKey)
	if err != nil {
		return nil, err
	}
	cur := "v" + version
	if _, err := update.ParseVersion(cur); err != nil {
		return nil, fmt.Errorf("this build: %w", err)
	}
	return &update.Updater{
		ManifestURL: releaseManifestURL,
		Name:        "raglit",
		Keys:        keys,
		Current:     cur,
		Exe:         exe + ".new",
		Home:        root,
		Platform:    update.Platform(runtime.GOOS, runtime.GOARCH),
		Fetch:       &update.Fetcher{},
	}, nil
}

// stageRelease runs one check. When it staged a new build, the build passed
// `--check` with args and is now exe; it returns the new version.
func stageRelease(u *update.Updater, exe string, args []string) (string, error) {
	res, err := u.Run(context.Background())
	if err != nil || !res.Staged {
		return "", err
	}
	staged := u.Exe
	if err := checkStaged(staged, args); err != nil {
		_ = os.Remove(staged)
		return "", fmt.Errorf("raglit %s does not start with this daemon's flags, so it is not installed: %w", res.To, err)
	}
	if err := os.Rename(staged, exe); err != nil {
		return "", err
	}
	return res.To, nil
}

// checkStaged runs the staged binary with this daemon's arguments plus
// --check, which parses the flags and exits without opening anything. A build
// that cannot read the flags the unit gives it would crash-loop under
// Restart=on-failure, so it is refused here instead.
func checkStaged(staged string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, staged, append(append([]string(nil), args...), "--check")...)
	cmd.Env = append(os.Environ(), "RAGLIT_NO_AUTOBUILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}
