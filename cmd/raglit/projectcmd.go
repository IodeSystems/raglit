package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

func runProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("project: create NAME | key NAME")
	}
	switch args[0] {
	case "create":
		return projectMint(args[1:], true)
	case "key":
		return projectMint(args[1:], false)
	case "delete":
		return projectDelete(args[1:])
	}
	return fmt.Errorf("project: unknown command %q (create, key, delete)", args[0])
}

func projectDelete(args []string) error {
	fs := flag.NewFlagSet("project delete", flag.ContinueOnError)
	homeOf, _ := addHomeFlags(fs)
	client := addClientFlags(fs)
	yes := fs.Bool("yes", false, "delete; without it, nothing happens")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("project delete: give one NAME")
	}
	if !*yes {
		return fmt.Errorf("project delete %s removes its indexes, watches, keys and cached work and cannot be undone: pass --yes", pos[0])
	}
	base, _, err := client(homeOf)
	if base == "" {
		return err
	}
	b, err := daemonDelete(base, "/api/projects", url.Values{"project": {pos[0]}})
	if err != nil {
		return err
	}
	var out struct {
		Project     string   `json:"project"`
		Indexes     []string `json:"indexes"`
		PoolEntries int      `json:"pool_entries"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	fmt.Printf("deleted project %s: %d index(es), %d cached document(s); its keys are revoked\n", out.Project, len(out.Indexes), out.PoolEntries)
	return nil
}

func projectMint(args []string, create bool) error {
	fs := flag.NewFlagSet("project", flag.ContinueOnError)
	homeOf, _ := addHomeFlags(fs)
	client := addClientFlags(fs)
	label := fs.String("label", "", "a note shown by `key list`")
	roots := fs.StringArray("root", nil, "a directory on the daemon's host this key may read and watch; repeat for more")
	asJSON := fs.Bool("json", false, `print {"project","url","token","key"} as JSON`)
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("project: give one NAME")
	}
	base, _, err := client(homeOf)
	if base == "" {
		return err
	}
	b, err := daemonPostJSON(base, "/api/projects/keys", map[string]any{
		"project": pos[0], "create": create, "label": *label, "roots": *roots,
	})
	if err != nil {
		return err
	}
	var resp struct {
		Token string     `json:"token"`
		Key   raglit.Key `json:"key"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	ns := strings.TrimPrefix(resp.Key.Principal, raglit.ProjectPrincipalPrefix)
	mcpURL := strings.TrimRight(base, "/") + "/mcp/" + ns
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"project": ns, "url": mcpURL, "token": resp.Token, "key": resp.Key,
		})
	}
	fmt.Printf("project: %s\nmcp:     %s\nkey:     %s   (shown once)\n", ns, mcpURL, resp.Token)
	return nil
}
