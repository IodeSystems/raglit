package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/iodesystems/raglit"
	flag "github.com/spf13/pflag"
)

// `raglit maintain` — the maintainer's run for a shared project: fold the
// public layers into the base (POST /api/projects/fold; see raglit/fold.go).
//
// Run it on a schedule as the maintainer's machine user, with a token from
// $RAGLIT_TOKEN or $RAGLIT_TOKEN_COMMAND. The maintainer must own the base:
// its current owner hands it over with POST /api/owner first.
func runMaintain(args []string) error {
	fs := flag.NewFlagSet("maintain", flag.ExitOnError)
	daemon := addDaemonFlag(fs)
	dry := fs.Bool("dry-run", false, "report what would be folded; write nothing")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("maintain: give one project id, or the project's namespace (what register returned)")
	}
	base, err := ensureDaemon(*daemon, raglit.DiscoverHome)
	if err != nil {
		return err
	}
	target := fs.Arg(0)
	body := map[string]any{"dry_run": *dry}
	// The argument is a project id or a namespace; try the id first and fall
	// back to the namespace when no project has that id.
	body["project_id"] = target
	b, err := daemonPostJSON(base, "/api/projects/fold", body)
	if err != nil && strings.Contains(err.Error(), "no project registered") {
		delete(body, "project_id")
		body["namespace"] = target
		b, err = daemonPostJSON(base, "/api/projects/fold", body)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		os.Stdout.Write(b)
		fmt.Println()
		return nil
	}
	var rep raglit.FoldReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return err
	}
	verb := "folded"
	if rep.DryRun {
		verb = "would fold"
	}
	fmt.Printf("project %s · base %s · %d public layer(s) read\n", rep.ProjectID, rep.Base, len(rep.LayersScanned))
	fmt.Printf("  %s %d document(s); %d already in the base\n", verb, len(rep.Folded), rep.AlreadyInBase)
	for _, f := range rep.Folded {
		fmt.Printf("    %s  ← %s\n", f.Layer, f.Source)
	}
	fmt.Printf("  provenance copied for %d earlier fold(s); %d still importing\n", rep.Settled, rep.Pending)
	for _, s := range rep.Skipped {
		fmt.Printf("  skipped: %s\n", s)
	}
	return nil
}
