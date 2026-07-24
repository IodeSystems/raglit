package raglit

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Source selection — resolve a project's config.Indexes to the concrete files to
// ingest. Rules layer project → index → root (ignore is UNIONed and always wins;
// include is overridable per root), plus each root's .gitignore and a built-in
// default that drops dot-entries and common vendor dirs.

// builtinIgnore is always applied: dot-files/dirs anywhere, plus node_modules and
// vendor trees. (Dot-dirs are also pruned during a non-git walk for speed.)
var builtinIgnore = []string{".*", "**/.*", "**/node_modules/**", "**/vendor/**"}

// PlanSources returns, per index name, the absolute file paths its configured
// roots + rules select. baseDir is the project directory (relative roots resolve
// against it). It shells out to `git ls-files` for a root's .gitignore semantics
// when the root is a git work tree and cfg.Gitignore isn't false.
func PlanSources(cfg Config, baseDir string) (map[string][]string, error) {
	useGitignore := cfg.Gitignore == nil || *cfg.Gitignore
	out := map[string][]string{}
	for name, idx := range cfg.Indexes {
		seen := map[string]bool{}
		var files []string
		for _, root := range idx.Roots {
			rootDir := root.Path
			if !filepath.IsAbs(rootDir) {
				rootDir = filepath.Join(baseDir, rootDir)
			}
			include := root.Include
			if len(include) == 0 {
				include = idx.Include
			}
			ignore := concatStrings(builtinIgnore, cfg.Ignore, idx.Ignore, root.Ignore)

			cands, err := candidateFiles(rootDir, useGitignore)
			if err != nil {
				return nil, err
			}
			for _, abs := range cands {
				rel, err := filepath.Rel(rootDir, abs)
				if err != nil {
					rel = filepath.Base(abs)
				}
				rel = filepath.ToSlash(rel)
				if len(include) > 0 && !matchAny(include, rel) {
					continue
				}
				if matchAny(ignore, rel) {
					continue
				}
				if !seen[abs] {
					seen[abs] = true
					files = append(files, abs)
				}
			}
		}
		sort.Strings(files)
		out[name] = files
	}
	return out, nil
}

// candidateFiles lists the files under rootDir before include/ignore filtering:
// git-tracked + untracked-not-ignored when it's a git work tree (so .gitignore is
// honored by git itself), else a plain walk that prunes dot-dirs.
func candidateFiles(rootDir string, useGitignore bool) ([]string, error) {
	if useGitignore && isGitWorkTree(rootDir) {
		out, err := exec.Command("git", "-C", rootDir, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
		if err == nil {
			var files []string
			for _, rel := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
				if rel != "" {
					files = append(files, filepath.Join(rootDir, rel))
				}
			}
			return files, nil
		}
		// git failed — fall back to a walk.
	}
	return walkFiles(rootDir)
}

func isGitWorkTree(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() == nil
}

func walkFiles(rootDir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(rootDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != rootDir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, p)
		return nil
	})
	return files, err
}

// matchAny reports whether rel (a slash path relative to its root) matches any
// glob. A glob with no "/" matches the basename (gitignore-style); one with "/"
// matches the whole relative path. "**" spans separators, "*" doesn't, "?" is one.
func matchAny(patterns []string, rel string) bool {
	for _, pat := range patterns {
		target := rel
		if !strings.Contains(pat, "/") {
			target = path.Base(rel)
		}
		if globRegexp(pat).MatchString(target) {
			return true
		}
	}
	return false
}

var (
	globMu    sync.Mutex
	globCache = map[string]*regexp.Regexp{}
)

func globRegexp(pattern string) *regexp.Regexp {
	globMu.Lock()
	defer globMu.Unlock()
	if re, ok := globCache[pattern]; ok {
		return re
	}
	re := regexp.MustCompile(globToRegexp(pattern))
	globCache[pattern] = re
	return re
}

func globToRegexp(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++ // consume the second '*'
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++                     // consume the slash
					b.WriteString("(?:.*/)?") // **/ → an optional directory prefix (so **/x matches x at root too)
				} else {
					b.WriteString(".*") // bare ** → anything, separators included
				}
			} else {
				b.WriteString("[^/]*") // * → within a path segment
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '[', ']', '{', '}', '^', '$', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	return b.String()
}

func concatStrings(ss ...[]string) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s...)
	}
	return out
}
