package main

import (
	"encoding/json"
	"fmt"
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
	}
	return fmt.Errorf("project: unknown command %q (create, key)", args[0])
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
