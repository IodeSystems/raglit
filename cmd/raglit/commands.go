package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Routing (USER 2026-10-23). cobra routes, prints the help, and completes
// command names (`raglit completion bash|zsh|fish`). Each command parses its
// own flags (DisableFlagParsing), so a command's FlagSet stays the one place
// its flags are defined. The help text is helptext.go.

// commands maps each command to its handler. Every name has a helptext.go
// entry, and the reverse (TestEveryCommandHasHelp).
var commands = map[string]func([]string) error{
	"init":                runInit,
	"index":               runIndexCmd,
	"ingest":              runIngest,
	"sync":                runSync,
	"import-readings":     runImportReadings,
	"migrate-attachments": runMigrateAttachments,
	"maintain":            runMaintain,
	"branch":              runBranch,
	"drop-index":          runDropIndex,
	"watch":               runWatch,
	"status":              runStatus,
	"reread":              runReread,
	"retry":               runRetry,
	"work":                runWork,
	"search":              runSearch,
	"similar":             runSimilar,
	"diff":                runDiff,
	"mark":                runMark,
	"marks":               runMarks,
	"withdraw":            runWithdraw,
	"withdrawn":           runWithdrawn,
	"forget":              runForget,
	"purge":               runPurge,
	"pause":               func(a []string) error { return runPause("pause", a) },
	"resume":              func(a []string) error { return runPause("resume", a) },
	"slice":               runSlice,
	"slices":              runSlices,
	"serve":               runServe,
	"daemon":              runDaemon,
	"service":             runService,
	"review":              runReview,
	"demo":                runDemo,
	"pagify":              runPagify,
	"refragment":          runRefragment,
	"regions":             runRegions,
	"region":              runRegion,
	"attest":              runAttest,
	"ocr":                 runOcr,
	"transcribe":          runTranscribe,
	"readings":            runReadings,
	"identify":            runIdentify,
	"audit-tags":          runAuditTags,
	"about":               runAbout,
	"hint":                runHint,
	"doctype":             runDocType,
	"fields":              runFields,
	"health":              runHealth,
	"doctor":              runDoctor,
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "raglit",
		Short:         "document index and search for LLM sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.SetHelpFunc(func(c *cobra.Command, _ []string) {
		if c == root {
			printRootHelp(os.Stdout)
			return
		}
		fmt.Fprintln(os.Stdout, c.Long)
	})
	for _, g := range helpGroups {
		id := g.name
		if id == "" {
			id = "common"
		}
		root.AddGroup(&cobra.Group{ID: id, Title: g.title})
		for _, e := range g.entries {
			run := commands[e.name]
			root.AddCommand(&cobra.Command{
				Use:                e.name,
				Short:              e.short,
				Long:               e.detail,
				GroupID:            id,
				DisableFlagParsing: true,
				RunE:               func(_ *cobra.Command, args []string) error { return run(args) },
			})
		}
	}
	root.SetHelpCommand(&cobra.Command{
		Use:                "help [GROUP|COMMAND|flags|all]",
		Short:              "help on a group or command",
		DisableFlagParsing: true,
		RunE:               func(_ *cobra.Command, args []string) error { return runHelp(args) },
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			topics := []string{"flags", "all"}
			for _, g := range helpGroups {
				if g.name != "" {
					topics = append(topics, g.name)
				}
				for _, e := range g.entries {
					topics = append(topics, e.name)
				}
			}
			return topics, cobra.ShellCompDirectiveNoFileComp
		},
	})
	return root
}
