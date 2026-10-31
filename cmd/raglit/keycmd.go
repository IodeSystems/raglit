package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/iodesystems/raglit"
)

// `raglit key` mints, lists and revokes API tokens and session keys
// (raglit/keys.go, cmd/raglit/keysapi.go). The caller's own credential
// ($RAGLIT_TOKEN, a token command, or a local daemon's admin token) decides
// what it may mint:
//
//	raglit key mint --principal aw4 --out FILE        API token (admin)
//	raglit key mint --session S [--project P]         session key
//	raglit key mint --branch [proj:]idx@S             child key (from a session key)
//
// A minted token is printed once, alone on stdout, or written to --out (0600)
// and not printed at all.
func runKey(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("key: one of mint | list | revoke")
	}
	switch args[0] {
	case "mint":
		return keyMint(args[1:])
	case "list", "ls":
		return keyList(args[1:])
	case "revoke":
		return keyRevoke(args[1:])
	}
	return fmt.Errorf("key: unknown subcommand %q — one of mint | list | revoke", args[0])
}

func keyMint(args []string) error {
	fs := flag.NewFlagSet("key mint", flag.ContinueOnError)
	homeOf, _ := addHomeFlags(fs)
	client := addClientFlags(fs)
	shared := addSharedFlag(fs)
	principal := fs.String("principal", "", "mint an API token acting as this principal (admin only)")
	session := fs.String("session", "", "mint a session key for this session id (e.g. aw4's tracking id)")
	branch := fs.String("branch", "", "mint a child key for this one session branch, [proj:]idx@session")
	label := fs.String("label", "", "a note shown by `key list`")
	out := fs.String("out", "", "write the token to this file (0600) instead of printing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := 0
	for _, v := range []string{*principal, *session, *branch} {
		if strings.TrimSpace(v) != "" {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("key mint: give exactly one of --principal, --session, --branch")
	}
	base, ns, err := client(homeOf)
	if base == "" {
		return err
	}
	body := map[string]any{"label": *label}
	switch {
	case *principal != "":
		body["principal"] = *principal
	case *session != "":
		if ns == "" {
			return fmt.Errorf("key mint --session: %w", err)
		}
		body["project"], body["session"] = ns, *session
		body["shared"] = projectShared(homeOf, shared()...)
	default:
		// "proj:idx@s" names that project's branch; a bare "idx@s" is this
		// project's. The daemon checks it against the minting key's grants.
		full, ok := nsNamedIndex(*branch)
		if !ok {
			if ns == "" {
				return fmt.Errorf("key mint --branch: name the project (proj:idx@session) or run in one")
			}
			full = nsIndex(ns, raglit.NormalizeIndexName(*branch))
		}
		body["branch"] = full
	}
	b, err := daemonPostJSON(base, "/api/keys", body)
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
	if *out != "" {
		if err := os.WriteFile(*out, []byte(resp.Token+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "wrote %s key %s (%s) to %s\n", resp.Key.Kind, resp.Key.ID, resp.Key.Principal, *out)
		return nil
	}
	fmt.Println(resp.Token)
	return nil
}

func keyList(args []string) error {
	fs := flag.NewFlagSet("key list", flag.ContinueOnError)
	homeOf, _ := addHomeFlags(fs)
	client := addClientFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base, _, err := client(homeOf)
	if base == "" {
		return err
	}
	b, err := daemonGet(base, "/api/keys", nil)
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(b))
		return nil
	}
	var resp struct {
		Keys []raglit.Key `json:"keys"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return err
	}
	if len(resp.Keys) == 0 {
		fmt.Println("no keys")
		return nil
	}
	for _, k := range resp.Keys {
		state := ""
		if k.Revoked {
			state = "  REVOKED"
		}
		fmt.Printf("%s  %-7s %s  issued by %s%s\n", k.ID, k.Kind, k.Principal, k.Issuer, state)
		if len(k.Write) > 0 {
			fmt.Printf("    writes: %s\n", strings.Join(k.Write, ", "))
		}
		if k.Label != "" {
			fmt.Printf("    %s\n", k.Label)
		}
	}
	return nil
}

func keyRevoke(args []string) error {
	fs := flag.NewFlagSet("key revoke", flag.ContinueOnError)
	homeOf, _ := addHomeFlags(fs)
	client := addClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("key revoke: name one key id (`raglit key list`)")
	}
	base, _, err := client(homeOf)
	if base == "" {
		return err
	}
	if _, err := daemonPostJSON(base, "/api/keys/revoke", map[string]any{"id": fs.Arg(0)}); err != nil {
		return err
	}
	fmt.Printf("revoked %s, and every key under it\n", fs.Arg(0))
	return nil
}
