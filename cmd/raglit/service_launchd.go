package main

import (
	"encoding/xml"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// raglit as a launchd user agent — `raglit service` on macOS.
//
// Same verbs as the systemd backend. The differences that matter:
//
//   - launchd starts an agent with PATH=/usr/bin:/bin:/usr/sbin:/sbin, which
//     does not hold Homebrew. Without the installer's PATH the daemon cannot
//     find pdftotext or pdftoppm, and every PDF fails as "tool not installed"
//     while `raglit doctor` in a shell says it is. So install records PATH
//     unless --env sets it.
//   - There is no ExecStartPre. The flags are validated by install (the same
//     `daemon --check`) before the plist is written.
//   - There is no journal. Output goes to ~/Library/Logs/raglit/NAME.log, and
//     `logs` reads its tail.

// launchdLabel is the agent label: reverse-DNS, as launchd expects.
func launchdLabel(name string) string { return "com.iodesystems." + name }

func launchdPlistPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(name)+".plist"), nil
}

func launchdLogPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "raglit", name+".log"), nil
}

// launchdTarget is the agent in the logged-in user's GUI domain.
func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func launchdTarget(name string) string { return launchdDomain() + "/" + launchdLabel(name) }

// refuseRoot stops `sudo raglit service …`. A LaunchAgent belongs to the
// logged-in user's GUI domain; as root, gui/0 does not exist, and launchctl
// answers "Bootstrap failed: 5" plus a hint to re-run as root — which is
// already the cause.
func refuseRoot() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run `raglit service` as your own user, not root or sudo: the agent runs in your login session")
	}
	return nil
}

// launchdBootout unloads the agent and waits until launchd has let it go.
// bootout returns before the job is gone (it may still be in ExitTimeOut), and
// a bootstrap in that window fails with error 5.
func launchdBootout(name string) {
	_ = exec.Command("launchctl", "bootout", launchdTarget(name)).Run()
	for deadline := time.Now().Add(130 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if exec.Command("launchctl", "print", launchdTarget(name)).Run() != nil {
			return
		}
	}
}

// launchdBootstrap loads the plist. On failure it names the cause instead of
// launchctl's "Bootstrap failed: 5: Input/output error", which says nothing.
func launchdBootstrap(name, path string) error {
	out, err := exec.Command("launchctl", "bootstrap", launchdDomain(), path).CombinedOutput()
	if err == nil {
		return nil
	}
	cause := "launchd refused it: " + strings.TrimSpace(firstLine(string(out)))
	switch {
	case exec.Command("launchctl", "print", launchdTarget(name)).Run() == nil:
		cause = "it is still loaded — the previous instance has not finished stopping; try `raglit service restart --name " + name + "`"
	case plistLint(path) != "":
		cause = "the plist is invalid: " + plistLint(path)
	default:
		if b, rerr := os.ReadFile(path); rerr == nil {
			if exe := plistExec(string(b)); exe != "" {
				if st, serr := os.Stat(exe); serr != nil || st.Mode()&0o111 == 0 {
					cause = exe + " is missing or not executable — reinstall with `raglit service install`"
				}
			}
		}
	}
	return fmt.Errorf("could not load %s: %s", launchdLabel(name), cause)
}

func firstLine(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }

// plistLint is plutil's complaint about path, "" when it is valid (or plutil
// is unavailable).
func plistLint(path string) string {
	out, err := exec.Command("plutil", "-lint", path).CombinedOutput()
	if err == nil || len(out) == 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// plistExec is the first ProgramArguments entry: the binary launchd runs.
func plistExec(plist string) string {
	_, rest, ok := strings.Cut(plist, "<key>ProgramArguments</key>")
	if !ok {
		return ""
	}
	_, rest, ok = strings.Cut(rest, "<string>")
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, "</string>")
	return html.UnescapeString(v)
}

func launchctl(args ...string) *exec.Cmd {
	c := exec.Command("launchctl", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c
}

func plistEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func plistString(s string) string { return "<string>" + plistEscape(s) + "</string>" }

// withPath adds PATH=path to envs unless envs already sets PATH.
func withPath(envs []string, path string) []string {
	for _, e := range envs {
		if strings.HasPrefix(e, "PATH=") {
			return envs
		}
	}
	return append(envs, "PATH="+path)
}

func renderPlist(name, execPath string, daemonArgs, envs []string, runAtLoad bool, logPath string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Generated by ` + "`raglit service install`" + `. Edits are overwritten on reinstall. -->
<plist version="1.0">
<dict>
`)
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t%s\n", plistString(launchdLabel(name)))
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range append([]string{execPath, "daemon"}, daemonArgs...) {
		fmt.Fprintf(&b, "\t\t%s\n", plistString(a))
	}
	b.WriteString("\t</array>\n")
	if len(envs) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, e := range envs {
			k, v, _ := strings.Cut(e, "=")
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t%s\n", plistEscape(k), plistString(v))
		}
		b.WriteString("\t</dict>\n")
	}
	// RunAtLoad is "start at login": the agent is loaded at every login, and
	// this decides whether loading also starts it.
	fmt.Fprintf(&b, "\t<key>RunAtLoad</key>\n\t<%t/>\n", runAtLoad)
	// Restart on a crash, not after a clean stop — systemd's Restart=on-failure.
	b.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	// An ingest job can be mid-document. Give it room to finish before SIGKILL.
	b.WriteString("\t<key>ExitTimeOut</key>\n\t<integer>120</integer>\n")
	fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t%s\n", plistString(logPath))
	fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t%s\n", plistString(logPath))
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func launchdInstall(name, execPath string, daemonArgs, envs []string, enable, start bool) error {
	if err := refuseRoot(); err != nil {
		return err
	}
	path, err := launchdPlistPath(name)
	if err != nil {
		return err
	}
	logPath, err := launchdLogPath(name)
	if err != nil {
		return err
	}
	for _, d := range []string{filepath.Dir(path), filepath.Dir(logPath)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	envs = withPath(envs, os.Getenv("PATH"))
	if err := os.WriteFile(path, []byte(renderPlist(name, execPath, daemonArgs, envs, enable, logPath)), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n  logs   %s\n", path, logPath)

	// Reload: an agent already loaded keeps its old definition until booted out.
	launchdBootout(name)
	_ = launchctl("enable", launchdTarget(name)).Run()
	if err := launchdBootstrap(name, path); err != nil {
		return err
	}
	if enable {
		fmt.Println("enabled at login")
	}
	if start && !enable { // RunAtLoad already started it
		if err := launchctl("kickstart", launchdTarget(name)).Run(); err != nil {
			return err
		}
	}
	if start || enable {
		fmt.Println("started")
	}
	return nil
}

func launchdUninstall(name string) error {
	if err := refuseRoot(); err != nil {
		return err
	}
	launchdBootout(name)
	path, err := launchdPlistPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed %s\n", path)
	return nil
}

// launchdLoaded ensures the agent is loaded, so start and restart work after
// a stop (which unloads it).
func launchdLoaded(name string) error {
	if exec.Command("launchctl", "print", launchdTarget(name)).Run() == nil {
		return nil
	}
	path, err := launchdPlistPath(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s is not installed — run `raglit service install` first", launchdLabel(name))
	}
	return launchdBootstrap(name, path)
}

func launchdCtl(verb, name string) error {
	if err := refuseRoot(); err != nil {
		return err
	}
	switch verb {
	case "start":
		if err := launchdLoaded(name); err != nil {
			return err
		}
		return launchctl("kickstart", launchdTarget(name)).Run()
	case "restart":
		if err := launchdLoaded(name); err != nil {
			return err
		}
		return launchctl("kickstart", "-k", launchdTarget(name)).Run()
	case "reload":
		// The main process only; it re-execs itself (reloadOnHUP).
		return launchctl("kill", "SIGHUP", launchdTarget(name)).Run()
	case "stop":
		// bootout, not kill: KeepAlive would restart a daemon that exits
		// non-zero on the signal. bootout sends SIGTERM and waits ExitTimeOut.
		launchdBootout(name)
		return nil
	default: // status
		return launchctl("print", launchdTarget(name)).Run()
	}
}

func launchdLogs(name, n string) error {
	p, err := launchdLogPath(name)
	if err != nil {
		return err
	}
	c := exec.Command("tail", "-n", n, p)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}
