package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// usage is the root help: the common commands and the groups, one screen.
func usage() { printRootHelp(os.Stderr) }

func printRootHelp(w io.Writer) {
	fmt.Fprint(w, "raglit — document index and search for LLM sessions\n\n")
	for _, g := range helpGroups {
		if g.name == "" {
			printEntries(w, g.entries)
		}
	}
	fmt.Fprint(w, "\nmore commands (raglit help GROUP):\n")
	for _, g := range helpGroups {
		if g.name != "" {
			fmt.Fprintf(w, "  %-12s %s\n", g.name, g.title)
		}
	}
	fmt.Fprint(w, "\nraglit help COMMAND   details     raglit COMMAND -h   flags\n"+
		"raglit help flags     flags shared by most commands\n"+
		"raglit completion bash|zsh|fish   shell completion script\n")
}

func printEntries(w io.Writer, es []helpEntry) {
	for _, e := range es {
		fmt.Fprintf(w, "  %-12s %s\n", e.name, e.short)
	}
}

// runHelp answers `raglit help [GROUP|COMMAND|flags|all]`.
func runHelp(args []string) error {
	w := os.Stdout
	if len(args) == 0 {
		printRootHelp(w)
		return nil
	}
	topic := strings.TrimSpace(args[0])
	switch topic {
	case "flags":
		fmt.Fprint(w, helpFlags)
		return nil
	case "all":
		for _, g := range helpGroups {
			for _, e := range g.entries {
				fmt.Fprintf(w, "%s\n\n", e.detail)
			}
		}
		fmt.Fprint(w, helpFlags)
		return nil
	}
	for _, g := range helpGroups {
		if g.name != "" && g.name == topic {
			fmt.Fprintf(w, "%s — %s\n\n", g.name, g.title)
			printEntries(w, g.entries)
			fmt.Fprint(w, "\nraglit help COMMAND for details.\n")
			return nil
		}
	}
	for _, g := range helpGroups {
		for _, e := range g.entries {
			if e.name == topic {
				fmt.Fprintf(w, "%s\n", e.detail)
				return nil
			}
		}
	}
	return fmt.Errorf("help: no command or group %q (raglit help lists them)", topic)
}

const helpFlags = `flags shared by most commands:
  --project NAME  this client's project; namespaces its indexes on the shared
                  daemon (default: config "project")
  --index NAME    the index (default: config default_index, else "default").
                  "proj:idx" names another project's index. Searching with
                  no --index reads the indexes scoped to this directory
                  and the ambient globals (raglit help index)
  --daemon URL    the daemon to use (or $RAGLIT_DAEMON; default: daemon.json,
                  else http://127.0.0.1:7420, auto-started)
  --home DIR      index home; default: nearest ./.raglit walking up from cwd,
                  else $RAGLIT_HOME or ~/local/raglit
  -n, --limit N   search/serve: max results
  --mode M        search: bm25 | vec | hybrid (default: hybrid where embedded, else bm25)
  --llm-url, --llm-model, --embed-model, --llm-key ($RAGLIT_LLM_KEY)
                  model endpoint overrides; default: config
`
