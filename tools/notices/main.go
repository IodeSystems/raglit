// Command notices checks the licenses of everything a raglit release ships and
// writes the third-party notices that go in the release tarball.
//
// A release binary links Go modules and embeds the web UI's npm packages. MIT
// and BSD require their copyright notice in every copy, binaries included, and
// Apache-2.0 also requires each NOTICE file; a minified bundle keeps none of
// them. So the tarball carries THIRD_PARTY_NOTICES.md, and a dependency under a
// license that is not on the allow-list fails the build (USER 2026-10-24: "a
// license check on all of the dependencies to ensure conformance").
//
// Run from the repo root, after `npm ci` in web/:
//
//	go run ./tools/notices -o THIRD_PARTY_NOTICES.md
//
// Go modules are listed with GOWORK=off: a developer's go.work swaps in local
// checkouts and other versions, and the release builds without one.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// allowed are the licenses raglit (MIT) can ship without further terms.
var allowed = map[string]bool{
	"MIT": true, "BSD-2-Clause": true, "BSD-3-Clause": true, "Apache-2.0": true,
	"ISC": true, "0BSD": true, "Unlicense": true, "CC0-1.0": true, "Public-Domain": true,
}

// reviewed are license files that classify as unknown and were read by hand.
// Keyed by module path and file name, so a new unknown file fails until
// someone reads it.
var reviewed = map[string]string{
	"modernc.org/sqlite LICENSE-SQLITE": "Public-Domain", // SQLite's public-domain dedication
	"modernc.org/memory LICENSE-LOGO":   "attribution for a logo image; not linked code",
}

// rules classify license text. Order matters: copyleft first, so a text that
// quotes another license is caught by the stricter one.
var rules = []struct {
	name string
	rx   *regexp.Regexp
}{
	{"AGPL", regexp.MustCompile(`(?i)GNU AFFERO GENERAL PUBLIC LICENSE`)},
	{"LGPL", regexp.MustCompile(`(?i)GNU LESSER GENERAL PUBLIC LICENSE|GNU LIBRARY GENERAL PUBLIC`)},
	{"GPL", regexp.MustCompile(`(?i)GNU GENERAL PUBLIC LICENSE`)},
	{"MPL-2.0", regexp.MustCompile(`(?i)Mozilla Public License,? (Version|v\.) ?2\.0`)},
	{"Apache-2.0", regexp.MustCompile(`(?i)Apache License,? Version 2\.0`)},
	{"BSD-3-Clause", regexp.MustCompile(`(?i)Neither the name|names of (its|the) contributors may be used`)},
	{"BSD-2-Clause", regexp.MustCompile(`(?i)Redistributions in binary form must reproduce`)},
	{"MIT", regexp.MustCompile(`(?i)Permission is hereby granted, free of charge`)},
	{"ISC", regexp.MustCompile(`(?i)Permission to use, copy, modify, and(/or)? distribute this software for any`)},
	{"Unlicense", regexp.MustCompile(`(?i)This is free and unencumbered software`)},
	{"CC0-1.0", regexp.MustCompile(`(?i)CC0 1\.0`)},
}

// classify names the license a text is, "" when no rule matches.
func classify(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	for _, r := range rules {
		if r.rx.MatchString(t) {
			return r.name
		}
	}
	return ""
}

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|unlicense)`)
var noticeFile = regexp.MustCompile(`(?i)^notice`)

// pkg is one shipped dependency.
type pkg struct {
	Kind, Name, Version, License string
	Texts                        []text
}

type text struct{ File, Body string }

func main() {
	out := flag.String("o", "", "write the notices here (default: check only)")
	flag.Parse()
	var pkgs []pkg
	var problems []string
	gp, gprob, err := goModules("./cmd/raglit")
	if err != nil {
		fail(err)
	}
	pkgs, problems = append(pkgs, gp...), append(problems, gprob...)
	np, nprob, err := npmPackages("web")
	if err != nil {
		fail(err)
	}
	pkgs, problems = append(pkgs, np...), append(problems, nprob...)
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "notices:", p)
		}
		fail(fmt.Errorf("%d dependency license problem(s)", len(problems)))
	}
	counts := map[string]int{}
	for _, p := range pkgs {
		counts[p.License]++
	}
	fmt.Fprintf(os.Stderr, "notices: %d dependencies, all allowed: %v\n", len(pkgs), counts)
	if *out != "" {
		if err := os.WriteFile(*out, render(pkgs), 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "notices:", err)
	os.Exit(1)
}

// goModules is every module the main package links, but the main module.
func goModules(mainPkg string) ([]pkg, []string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", mainPkg)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("go list: %w", err)
	}
	seen := map[string]bool{}
	var pkgs []pkg
	var problems []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		f := strings.Split(line, "\t")
		if len(f) != 3 || f[2] == "" {
			problems = append(problems, "go module with no source directory: "+line)
			continue
		}
		p, prob := goModule(f[0], f[1], f[2])
		pkgs, problems = append(pkgs, p), append(problems, prob...)
	}
	return pkgs, problems, nil
}

// goModule reads one module's license and NOTICE files. Every license file
// must classify as allowed or be reviewed; the module's license is its first
// classified one.
func goModule(path, version, dir string) (pkg, []string) {
	p := pkg{Kind: "go", Name: path, Version: version}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return p, []string{fmt.Sprintf("%s: %v", path, err)}
	}
	var problems []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !(licenseFile.MatchString(name) || noticeFile.MatchString(name)) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", path, name, err))
			continue
		}
		p.Texts = append(p.Texts, text{File: name, Body: string(b)})
		if noticeFile.MatchString(name) {
			continue
		}
		lic := classify(string(b))
		if lic == "" {
			if r, ok := reviewed[path+" "+name]; ok {
				if allowed[r] && p.License == "" {
					p.License = r
				}
				continue
			}
			problems = append(problems, fmt.Sprintf("%s %s: license text not recognised; read it and add it to reviewed", path, name))
			continue
		}
		if !allowed[lic] {
			problems = append(problems, fmt.Sprintf("%s %s: %s is not on the allow-list", path, name, lic))
		}
		if p.License == "" {
			p.License = lic
		}
	}
	if p.License == "" && len(problems) == 0 {
		problems = append(problems, path+": no license file")
	}
	return p, problems
}

// npmPackages is the web UI's production dependency tree: what the bundle can
// contain. Dev tools (vite, tsc) build it and are not shipped.
func npmPackages(webDir string) ([]pkg, []string, error) {
	cmd := exec.Command("npm", "ls", "--omit=dev", "--all", "--json", "--long")
	cmd.Dir = webDir
	b, err := cmd.Output()
	if err != nil && len(b) == 0 {
		return nil, nil, fmt.Errorf("npm ls in %s (run npm ci there first): %w", webDir, err)
	}
	var root npmNode
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("npm ls: %w", err)
	}
	seen := map[string]bool{}
	var pkgs []pkg
	var problems []string
	var walk func(map[string]npmNode)
	walk = func(deps map[string]npmNode) {
		names := make([]string, 0, len(deps))
		for n := range deps {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			d := deps[n]
			key := n + "@" + d.Version
			if seen[key] {
				continue
			}
			seen[key] = true
			if d.Missing || d.Path == "" {
				problems = append(problems, "npm package not installed: "+key)
				continue
			}
			p := pkg{Kind: "npm", Name: n, Version: d.Version, License: spdx(d.License)}
			if ents, err := os.ReadDir(d.Path); err == nil {
				for _, e := range ents {
					if !e.IsDir() && (licenseFile.MatchString(e.Name()) || noticeFile.MatchString(e.Name())) {
						if b, err := os.ReadFile(filepath.Join(d.Path, e.Name())); err == nil {
							p.Texts = append(p.Texts, text{File: e.Name(), Body: string(b)})
						}
					}
				}
			}
			if !npmAllowed(p.License) {
				problems = append(problems, fmt.Sprintf("npm %s: license %q is not on the allow-list", key, p.License))
			}
			pkgs = append(pkgs, p)
			walk(d.Dependencies)
		}
	}
	walk(root.Dependencies)
	return pkgs, problems, nil
}

type npmNode struct {
	Version      string             `json:"version"`
	Path         string             `json:"path"`
	License      any                `json:"license"`
	Missing      bool               `json:"missing"`
	Dependencies map[string]npmNode `json:"dependencies"`
}

// spdx is a package.json license field as one string.
func spdx(v any) string {
	switch l := v.(type) {
	case string:
		return l
	case map[string]any:
		s, _ := l["type"].(string)
		return s
	}
	return ""
}

// npmAllowed accepts an SPDX expression whose every alternative of an OR, or
// every part of an AND, is allowed.
func npmAllowed(expr string) bool {
	e := strings.Trim(strings.TrimSpace(expr), "()")
	if e == "" {
		return false
	}
	if parts := strings.Split(e, " OR "); len(parts) > 1 {
		for _, p := range parts {
			if npmAllowed(p) {
				return true
			}
		}
		return false
	}
	for _, p := range strings.Split(e, " AND ") {
		if !allowed[strings.Trim(strings.TrimSpace(p), "()")] {
			return false
		}
	}
	return true
}

func render(pkgs []pkg) []byte {
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].Kind != pkgs[j].Kind {
			return pkgs[i].Kind < pkgs[j].Kind
		}
		return pkgs[i].Name < pkgs[j].Name
	})
	var b bytes.Buffer
	b.WriteString("# Third-party notices\n\n")
	b.WriteString("raglit is MIT licensed (LICENSE). This release also contains the software\n")
	b.WriteString("below, each under its own license, reproduced here as those licenses require.\n")
	b.WriteString("Generated by tools/notices.\n\n")
	for _, p := range pkgs {
		fmt.Fprintf(&b, "- %s %s %s — %s\n", p.Kind, p.Name, p.Version, p.License)
	}
	for _, p := range pkgs {
		fmt.Fprintf(&b, "\n---\n\n## %s %s (%s)\n", p.Name, p.Version, p.License)
		if len(p.Texts) == 0 {
			fmt.Fprintf(&b, "\nLicense: %s (declared in package.json; no license file shipped)\n", p.License)
		}
		for _, t := range p.Texts {
			fmt.Fprintf(&b, "\n### %s\n\n```\n%s\n```\n", t.File, strings.TrimRight(t.Body, "\n"))
		}
	}
	return b.Bytes()
}
