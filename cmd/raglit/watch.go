package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/iodesystems/raglit"
)

// `raglit watch` registers this project with the daemon for auto re-ingest on
// change (the daemon does the watching; see watcher.go). The project must have
// `indexes` (roots) configured; set `"watch": true` in config so it survives and
// re-registers via `raglit sync`. Subcommands:
//
//	raglit watch [start]   register this project home (idempotent)
//	raglit watch list      list all homes the daemon is watching
//	raglit watch stop      unregister this project home
func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	homeFlag := fs.String("home", "", "index home dir (default: nearest ./.raglit)")
	client := addClientFlags(fs) // --daemon + --embedded + --project
	fs.Parse(args)

	homeOf := func() raglit.Home {
		if *homeFlag != "" {
			return raglit.Home(*homeFlag)
		}
		return raglit.DiscoverHome()
	}
	dURL, _, err := client(homeOf, false)
	if err != nil {
		return err
	}
	if dURL == "" {
		return fmt.Errorf("watch: the daemon does the watching — remove --embedded/--db")
	}

	sub := fs.Arg(0)
	if sub == "" {
		sub = "start"
	}
	switch sub {
	case "list", "ls":
		return watchList(dURL)
	case "start", "add":
		home, err := filepath.Abs(string(homeOf()))
		if err != nil {
			return err
		}
		if _, err := daemonPostJSON(dURL, "/api/watch", map[string]any{"home": home}); err != nil {
			return err
		}
		fmt.Printf("watching %s (daemon re-ingests on change)\n", home)
		return nil
	case "stop", "rm", "remove":
		home, err := filepath.Abs(string(homeOf()))
		if err != nil {
			return err
		}
		if _, err := daemonDelete(dURL, "/api/watch", url.Values{"home": {home}}); err != nil {
			return err
		}
		fmt.Printf("stopped watching %s\n", home)
		return nil
	default:
		return fmt.Errorf("watch: unknown subcommand %q (start | list | stop)", sub)
	}
}

func watchList(base string) error {
	b, err := daemonGet(base, "/api/watch", nil)
	if err != nil {
		return err
	}
	var resp struct {
		Watches []struct {
			Home     string `json:"home"`
			Project  string `json:"project"`
			Watching bool   `json:"watching"`
			Files    int    `json:"files"`
		} `json:"watches"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	if len(resp.Watches) == 0 {
		fmt.Println("(no watched projects)")
		return nil
	}
	for _, wi := range resp.Watches {
		state := "active"
		if !wi.Watching {
			state = "paused (watch:false)"
		}
		fmt.Printf("%-10s %d file(s)  [%s]  %s\n", wi.Project, wi.Files, state, wi.Home)
	}
	return nil
}
