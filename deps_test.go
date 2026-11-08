package raglit

import (
	"os/exec"
	"strings"
	"testing"
)

// The root package is the engine, and a daemon that embeds it must not drag the
// HTTP, auth and MCP stack along. This fails when one of them enters its import
// closure — most likely through a subpackage that grew an HTTP dependency.
func TestRootDepsHaveNoServerStack(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	out, err := exec.Command(goBin, "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list -deps: %v", err)
	}
	// Module path prefixes, not substrings: go-humanize is not huma.
	banned := []string{
		"github.com/danielgtaylor/huma",
		"github.com/go-chi/chi",
		"github.com/coreos/go-oidc",
		"github.com/zitadel/oidc",
		"golang.org/x/oauth2",
		"github.com/modelcontextprotocol",
		"github.com/mark3labs/mcp-go",
		"github.com/iodesystems/raglit/cmd",
		"github.com/iodesystems/raglit/attest/service",
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, b := range banned {
			if pkg == b || strings.HasPrefix(pkg, b+"/") {
				t.Errorf("root package imports %s (via the banned %s)", pkg, b)
			}
		}
		if strings.Contains(pkg, "/mcp") || strings.Contains(pkg, "oidc") {
			t.Errorf("root package imports %s", pkg)
		}
	}
}
