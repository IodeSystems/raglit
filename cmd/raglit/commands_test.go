package main

import "testing"

// A command without a help entry is unreachable (rootCmd routes from
// helptext.go), and an entry without a handler is a command that does nothing.
func TestEveryCommandHasHelp(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range helpGroups {
		for _, e := range g.entries {
			if commands[e.name] == nil {
				t.Errorf("help entry %q has no handler in commands", e.name)
			}
			if seen[e.name] {
				t.Errorf("help entry %q appears twice", e.name)
			}
			seen[e.name] = true
		}
	}
	for name := range commands {
		if !seen[name] {
			t.Errorf("command %q has no helptext.go entry, so nothing routes to it", name)
		}
	}
}

// "serve" is the MCP server's old name; configs written before "mcp" still
// launch it.
func TestServeStillRoutesToMCP(t *testing.T) {
	c, _, err := rootCmd().Find([]string{"serve"})
	if err != nil || c.Name() != "mcp" {
		t.Fatalf("raglit serve routes to %v (err %v), want mcp", c, err)
	}
}
