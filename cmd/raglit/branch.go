package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/iodesystems/raglit"
)

// `raglit branch` manages copy-on-write branch indexes on the shared daemon
// (worktree-style overlays). Branch names ARE index names, so they're namespaced
// by project exactly like every other index: the daemon sees "<project>__<name>",
// and `list` shows only THIS project's branches (foreign ones are filtered out).
// Branches only exist in the daemon's scoped storage, so this always routes to
// the daemon — --embedded is refused.
//
// Flags precede the subcommand (standard flag parsing):
//
//	raglit branch [--home DIR] list
//	raglit branch [--home DIR] [--parent PARENT] fork NAME   (parent defaults to "default")
//	raglit branch [--home DIR] delete NAME
func runBranch(args []string) error {
	fs := flag.NewFlagSet("branch", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs) // --daemon + --embedded + --project
	parent := fs.String("parent", "", `parent index to fork from (fork only; default "default")`)
	fs.Parse(args)

	sub := fs.Arg(0)
	if sub == "" {
		return fmt.Errorf("branch: need a subcommand (list | fork NAME | delete NAME)")
	}

	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}

	dURL, ns, err := client(homeOf, false)
	if err != nil {
		return err
	}
	if dURL == "" {
		return fmt.Errorf("branch: branches live in the daemon's scoped storage — remove --embedded/--db")
	}

	switch sub {
	case "list", "ls":
		return branchList(dURL, ns)
	case "fork", "create":
		if fs.NArg() != 2 {
			return fmt.Errorf("branch fork: need exactly one NAME")
		}
		return branchFork(dURL, ns, fs.Arg(1), *parent)
	case "delete", "rm", "del":
		if fs.NArg() != 2 {
			return fmt.Errorf("branch delete: need exactly one NAME")
		}
		return branchDelete(dURL, ns, fs.Arg(1))
	default:
		return fmt.Errorf("branch: unknown subcommand %q (list | fork | delete)", sub)
	}
}

func branchFork(base, ns, name, parent string) error {
	b, err := daemonPostJSON(base, "/api/branches", map[string]any{
		"name":   nsIndex(ns, name),
		"parent": nsIndex(ns, parent), // empty parent → "<ns>__default"
	})
	if err != nil {
		return err
	}
	var r struct {
		Name   string `json:"name"`
		Parent string `json:"parent"`
		OK     bool   `json:"ok"`
	}
	_ = json.Unmarshal(b, &r)
	fmt.Printf("forked branch %q off %q\n", nsStrip(ns, r.Name), nsStrip(ns, r.Parent))
	return nil
}

func branchDelete(base, ns, name string) error {
	if _, err := daemonDelete(base, "/api/branches", url.Values{"name": {nsIndex(ns, name)}}); err != nil {
		return err
	}
	fmt.Printf("deleted branch %q\n", name)
	return nil
}

func branchList(base, ns string) error {
	b, err := daemonGet(base, "/api/branches", nil)
	if err != nil {
		return err
	}
	var resp struct {
		Branches []raglit.BranchInfo `json:"branches"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	prefix := ns + nsSep
	var mine []raglit.BranchInfo
	for _, bi := range resp.Branches {
		if ns != "" && !strings.HasPrefix(bi.Name, prefix) {
			continue // another project's branch
		}
		bi.Name = nsStrip(ns, bi.Name)
		bi.Parent = nsStrip(ns, bi.Parent)
		mine = append(mine, bi)
	}
	if len(mine) == 0 {
		fmt.Println("(no branches)")
		return nil
	}
	now := time.Now()
	for _, bi := range mine {
		fmt.Printf("%-20s ← %-16s  %d doc(s)  age %s  last-access %s\n",
			bi.Name, bi.Parent, bi.Documents,
			since(now, bi.CreatedAt), since(now, bi.LastAccessedAt))
	}
	return nil
}

// since renders a compact age from a UnixNano timestamp (0 → "n/a").
func since(now time.Time, unixNano int64) string {
	if unixNano == 0 {
		return "n/a"
	}
	d := now.Sub(time.Unix(0, unixNano))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
