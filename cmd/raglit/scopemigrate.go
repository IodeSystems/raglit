package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/iodesystems/raglit"
)

// migrateScopes gives every index whose scopes were never set a first answer,
// once (USER 2026-10-23: existing project indexes are scoped to their
// project's directory). Before scopes, a project's indexes were found by name
// ("<project>__*"); a search now finds them by the directory it runs in.
//
// The project directory comes from a watched project's home. Failing that, the
// index's document root: the directory all its documents lie under, which for
// a project index is the project or a part of it. A root of /, $HOME or above
// is no evidence of a project, and an unnamespaced index has no project, so
// those become opt-in globals: read only when named.
func migrateScopes(reg *raglit.Registry, watchedHomes []string) {
	projectDir := map[string]string{}
	daemonHomes := filepath.Join(raglit.DefaultRoot(), "projects") + "/"
	for _, h := range watchedHomes {
		if strings.HasPrefix(h, daemonHomes) {
			continue // a home watchHome made for a caller with no .raglit: no project dir
		}
		if cfg, ok, _ := raglit.LoadConfig(raglit.Home(h)); ok && cfg.Project != "" {
			projectDir[raglit.NormalizeIndexName(cfg.Project)] = filepath.Dir(h)
		}
	}
	home, _ := os.UserHomeDir()
	for _, name := range reg.Names() {
		st, err := reg.Existing(name)
		if err != nil {
			continue
		}
		if _, set := st.Scopes(); set {
			continue
		}
		ns, _, namespaced := strings.Cut(name, nsSep)
		scope := projectDir[ns]
		if scope == "" && namespaced {
			if root, err := indexRoot(st); err == nil && projectLike(root, home) {
				scope = root
			}
		}
		scopes := []string{}
		if scope != "" {
			scopes = []string{scope}
		}
		if _, err := st.SetScopes(scopes); err != nil {
			log.Printf("raglit: scopes for %s: %v", name, err)
			continue
		}
		if scope == "" {
			log.Printf("raglit: index %s: no project directory known — global, read only when named", name)
		} else {
			log.Printf("raglit: index %s: scoped to %s", name, scope)
		}
	}
}

// projectLike reports whether a document root can stand for a project: not
// empty, not /, and not the home directory or anything above it.
func projectLike(root, home string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	root = filepath.Clean(root)
	if root == "/" {
		return false
	}
	if home != "" && (root == home || strings.HasPrefix(home, root+"/")) {
		return false
	}
	return true
}
