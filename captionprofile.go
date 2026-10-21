package raglit

import (
	"path/filepath"
	"strings"
)

// Caption profiles: different caption rules for different kinds of file.
//
// One prompt written for a case folder ("cataloguing a document for a
// legal/records index") was asked about Go source, and could only answer
// "deed" or "agreement" — or fail. 7 code files were stored as deeds and 25 as
// agreements before anybody looked (USER 2026-10-20: "kinds are important";
// "when it's code, it should be about the purpose, component"). So the rules
// are per profile, chosen by FILE TYPE: what a caption should say about a
// source file (its purpose and component) has nothing in common with what it
// should say about an instrument (its date, parties and property).
//
// Kinds are "family:kind" — legal:deed, code:entrypoint, docs:plan — so a
// list can group by family and no two profiles' vocabularies collide. Each
// profile's list is CLOSED, for the reason identityKinds gives.

// kindDef is one term of a profile's vocabulary and the line the prompt shows
// for it.
type kindDef struct {
	Name string // family:kind
	Desc string
}

// captionProfile is everything about a caption that depends on what the file is.
type captionProfile struct {
	Family string
	// Framing opens the prompt: what is being catalogued, for whom.
	Framing string
	// NameRule and SummaryRule describe the two prose fields.
	NameRule    string
	SummaryRule string
	// NameExample is shown as the shape of a name; ExampleWords are words in it
	// that belong to no real file, so a copied example can be caught.
	NameExample  string
	ExampleWords []string
	// CopiedMsg and ThinSummaryMsg go back to the model when it copies the
	// example or writes a summary that distinguishes nothing.
	CopiedMsg      string
	ThinSummaryMsg string
	// TagExamples are example content tags in this profile's terms.
	TagExamples string
	Kinds       []kindDef
	// Aliases map what a model actually returns onto Kinds (lowercased).
	Aliases map[string]string
	// ShowPath: the file's path is part of what it is (code, docs), so the
	// prompt carries it. Never for records — see Identify on why filenames lie.
	ShowPath bool
}

func (p *captionProfile) kindNames() []string {
	out := make([]string, len(p.Kinds))
	for i, k := range p.Kinds {
		out[i] = k.Name
	}
	return out
}

// normalize maps a model's kind onto this profile's vocabulary: the full
// "family:kind", the bare kind, a spelling with spaces, or an alias.
func (p *captionProfile) normalize(s string) (string, bool) {
	k := strings.Trim(strings.ToLower(strings.TrimSpace(s)), ".")
	if k == "" {
		return "", false
	}
	if a, ok := p.Aliases[k]; ok {
		return a, true
	}
	bare := strings.TrimPrefix(k, p.Family+":")
	bare = strings.Join(strings.Fields(bare), "-")
	want := p.Family + ":" + bare
	for _, d := range p.Kinds {
		if d.Name == want {
			return want, true
		}
	}
	if a, ok := p.Aliases[strings.ReplaceAll(bare, "-", " ")]; ok {
		return a, true
	}
	return "", false
}

// kindBlock is the prompt's list of this profile's kinds.
func (p *captionProfile) kindBlock() string {
	w := 0
	for _, k := range p.Kinds {
		w = max(w, len(k.Name))
	}
	var b strings.Builder
	for _, k := range p.Kinds {
		b.WriteString("    " + k.Name + strings.Repeat(" ", w-len(k.Name)) + " — " + k.Desc + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// copiesExample reports whether name carries the example's words while the
// file's own text has none of them — the example copied, not this file named.
func (p *captionProfile) copiesExample(name, text string) bool {
	n, t := strings.ToLower(name), strings.ToLower(text)
	inName := false
	for _, w := range p.ExampleWords {
		if strings.Contains(t, w) {
			return false
		}
		inName = inName || strings.Contains(n, w)
	}
	return inName
}

var legalProfile = &captionProfile{
	Family:  "legal",
	Framing: "You are cataloguing a document for a legal/records index.",
	NameRule: `what a person filing this would call it — the instrument, its date,
  and the parties or property it concerns. A caption for a list, not a filename
  and not a sentence.`,
	SummaryRule: `a few sentences — what the instrument IS, who the parties are, the
  date, the property or matter it concerns, and what it does. Enough that someone
  reading only this can tell whether they need the document.`,
	NameExample:    identityNameExample,
	ExampleWords:   identityExampleParties,
	CopiedMsg:      `"name" copies the example from the instructions; name THIS document from its own dates, instrument and parties`,
	ThinSummaryMsg: `"summary" says nothing that distinguishes this document from another; state the instrument, the parties, the date and what it does`,
	TagExamples:    `"lead paint inspection", "boundary survey", "escrow closing"`,
	Kinds: []kindDef{
		{"legal:deed", "an instrument conveying or encumbering land"},
		{"legal:survey", "a survey, plat or map of land"},
		{"legal:agreement", "a contract between parties, signed or offered"},
		{"legal:correspondence", "a letter, email, memo or notice between people"},
		{"legal:court-filing", "anything filed in or issued by a court"},
		{"legal:certification", "a certificate, affidavit, permit or official attestation"},
		{"legal:analysis", "a report, assessment, inspection or study of something"},
		// notes and commercial: see identityKinds for why each was added.
		{"legal:notes", "a document somebody MADE about the matter rather than one filed in it: a timeline, a witness list, a call transcript, a packet assembled for counsel, a worklist"},
		{"legal:commercial", "a record of work done or goods sold: an invoice, a work order, a receipt, a statement, a property listing"},
		{"legal:other", `none of the above genuinely fits. Prefer any term above to this one; "other" is a last resort, not a default`},
	},
	Aliases: prefixAliases("legal", identityKindAliases),
}

var codeProfile = &captionProfile{
	Family:  "code",
	Framing: "You are cataloguing a file in a software project's source code. Its path is given above the file; the path is part of what it is.",
	NameRule: `the file's PURPOSE and the COMPONENT it belongs to — what it does and
  where it sits in the project. Not the language, not "source code", and not the
  file name restated.`,
	SummaryRule: `a few sentences — what this file is for, which component or package
  it belongs to, what it exposes (its main types, functions, commands, routes or
  components) and what calls it or what it depends on. Enough that someone reading
  only this knows whether to open the file.`,
	NameExample:    "Tidewater billing — invoice webhook receiver and retry ledger",
	ExampleWords:   []string{"tidewater"},
	CopiedMsg:      `"name" copies the example from the instructions; name THIS file from its own purpose and component`,
	ThinSummaryMsg: `"summary" says nothing that distinguishes this file from another; state what it is for, its component and what it exposes`,
	TagExamples:    `"job queue", "oidc login", "schema migrations"`,
	Kinds: []kindDef{
		{"code:entrypoint", "where a program starts: a main function, CLI command dispatch, server startup and wiring"},
		{"code:library", "implementation other code calls: logic, data structures, helpers"},
		{"code:api", "the surface a client calls: HTTP/RPC handlers, routes, a public SDK"},
		{"code:ui", "renders an interface: components, pages, views, styles"},
		{"code:schema", "the shape of stored data: database schema, migrations, queries, data type definitions"},
		{"code:test", "tests, fixtures and test helpers"},
		{"code:generated", "written by a tool, not by hand (says so in a header, or is a lockfile/sum)"},
		{"code:config", "settings read at run time: configuration, environment, deployment manifests"},
		{"code:build", "builds or packages the project: build files, dependency manifests, CI workflows"},
		{"code:script", "a script run by hand or by CI to do a task"},
		{"code:other", "none of the above genuinely fits; a last resort"},
	},
	Aliases: map[string]string{
		"main": "code:entrypoint", "cli": "code:entrypoint", "command": "code:entrypoint",
		"lib": "code:library", "implementation": "code:library", "source": "code:library", "module": "code:library", "package": "code:library", "util": "code:library", "utility": "code:library",
		"handler": "code:api", "server": "code:api", "routes": "code:api", "endpoint": "code:api",
		"component": "code:ui", "frontend": "code:ui", "view": "code:ui", "page": "code:ui", "style": "code:ui", "stylesheet": "code:ui", "css": "code:ui",
		"migration": "code:schema", "sql": "code:schema", "model": "code:schema", "types": "code:schema",
		"tests": "code:test", "fixture": "code:test", "spec": "code:test",
		"lockfile": "code:generated", "codegen": "code:generated",
		"configuration": "code:config", "settings": "code:config", "manifest": "code:config",
		"makefile": "code:build", "ci": "code:build", "dependencies": "code:build",
		"shell script": "code:script", "automation": "code:script",
	},
	ShowPath: true,
}

var docsProfile = &captionProfile{
	Family:  "docs",
	Framing: "You are cataloguing a document in a software project: prose written about the project, not code. Its path is given above the document.",
	NameRule: `what the document is and the component or subject it covers. A caption
  for a list, not a sentence and not the file name restated.`,
	SummaryRule: `a few sentences — what the document covers, which part of the
  project it concerns, and what a reader gets from it (a decision, a status, a
  procedure, a reference). Enough that someone reading only this knows whether to
  open it.`,
	NameExample:    "Tidewater billing — webhook retry design and open questions",
	ExampleWords:   []string{"tidewater"},
	CopiedMsg:      `"name" copies the example from the instructions; name THIS document from its own subject`,
	ThinSummaryMsg: `"summary" says nothing that distinguishes this document from another; state what it covers and what a reader gets from it`,
	TagExamples:    `"job queue", "oidc login", "schema migrations"`,
	Kinds: []kindDef{
		{"docs:readme", "the front page of a project or folder: what it is, how to start"},
		{"docs:plan", "work planned or in progress: a roadmap, a status, a backlog"},
		{"docs:design", "how something is or will be built, and why: an architecture note, a proposal, a decision record"},
		{"docs:spec", "what something must do: a specification, a protocol, a contract between parts"},
		{"docs:guide", "how to do something, step by step"},
		{"docs:reference", "looked up, not read through: an API listing, a cheat sheet, a glossary"},
		{"docs:changelog", "what changed and when"},
		{"docs:notes", "working notes, findings, a log of an investigation"},
		{"docs:other", "none of the above genuinely fits; a last resort"},
	},
	Aliases: map[string]string{
		"overview": "docs:readme", "roadmap": "docs:plan", "status": "docs:plan", "backlog": "docs:plan",
		"architecture": "docs:design", "proposal": "docs:design", "adr": "docs:design", "rfc": "docs:design",
		"specification": "docs:spec", "protocol": "docs:spec",
		"how-to": "docs:guide", "tutorial": "docs:guide", "manual": "docs:guide",
		"cheat sheet": "docs:reference", "glossary": "docs:reference", "documentation": "docs:reference",
		"changes": "docs:changelog", "release notes": "docs:changelog",
		"log": "docs:notes", "report": "docs:notes", "findings": "docs:notes",
	},
	ShowPath: true,
}

var imageProfile = &captionProfile{
	Family:  "image",
	Framing: "You are cataloguing an image. What it shows was read from it and is given below.",
	NameRule: `what the image shows — its subject, and the place or date when the image
  states them. A caption for a list, not a sentence.`,
	SummaryRule: `a few sentences — what is shown, anything legible in it (signs,
  labels, dates, names), and what it appears to record. For a scan or photo of a
  paper document, say what the document is.`,
	NameExample:    "Tidewater pier pump house, north wall, after the storm",
	ExampleWords:   []string{"tidewater"},
	CopiedMsg:      `"name" copies the example from the instructions; name THIS image from what it shows`,
	ThinSummaryMsg: `"summary" says nothing that distinguishes this image from another; state what is shown and anything legible in it`,
	TagExamples:    `"driveway", "property line", "septic tank"`,
	Kinds: []kindDef{
		{"image:photo", "a photograph of a place, object or people"},
		{"image:screenshot", "a capture of a screen: an app, a web page, a message"},
		{"image:diagram", "a drawing that explains: a chart, a sketch, a schematic, a logo or icon"},
		{"image:map", "a map, aerial view or site plan"},
		{"image:scan", "a photo or scan of a paper document"},
		{"image:other", "none of the above genuinely fits; a last resort"},
	},
	Aliases: map[string]string{
		"photograph": "image:photo", "picture": "image:photo",
		"screen capture": "image:screenshot", "chart": "image:diagram", "drawing": "image:diagram", "logo": "image:diagram", "icon": "image:diagram",
		"aerial": "image:map", "site plan": "image:map", "plat": "image:map",
		"document": "image:scan", "scanned document": "image:scan",
	},
}

var captionProfiles = []*captionProfile{legalProfile, codeProfile, docsProfile, imageProfile}

func prefixAliases(family string, m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = family + ":" + strings.Join(strings.Fields(v), "-")
	}
	return out
}

// codeExts are source files; codeNames are build/config files with no extension
// (or one that says nothing).
var codeExts = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rb": true, ".rs": true, ".java": true, ".kt": true, ".swift": true, ".c": true,
	".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".php": true, ".lua": true,
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
	".sql": true, ".proto": true, ".graphql": true,
	".css": true, ".scss": true, ".less": true, ".vue": true, ".svelte": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".env": true,
	".mod": true, ".sum": true, ".lock": true, ".star": true, ".bzl": true, ".nix": true,
	".tf": true, ".hcl": true, ".gradle": true, ".webmanifest": true,
}

var codeNames = map[string]bool{
	"makefile": true, "dockerfile": true, "justfile": true, "rakefile": true, "gemfile": true,
	"procfile": true, "containerfile": true, ".gitignore": true, ".dockerignore": true, ".editorconfig": true,
}

// captionImageExts are images to caption as images: extract.go's imageExts
// plus the ones it reads another way (HEIC transcoded, SVG as text).
var captionImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true, ".heif": true,
	".tif": true, ".tiff": true, ".bmp": true, ".svg": true,
}

// projectOnlyExts are code in a software project and records elsewhere: an
// HTML file in a case folder is a saved page or an email body, not a UI.
var projectOnlyExts = map[string]bool{".html": true, ".htm": true, ".xml": true}

var proseExts = map[string]bool{".md": true, ".markdown": true, ".rst": true, ".txt": true, ".adoc": true, ".org": true}

// isCodePath reports whether path names a source/build file.
func isCodePath(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimPrefix(path, "file://")))
	return codeExts[filepath.Ext(base)] || codeNames[base]
}

// profileFor picks the caption rules for one file. codeCorpus says the index
// is a software project (codeCorpusShare), which is what makes a .md file
// project documentation rather than a case note.
func profileFor(path string, codeCorpus bool) *captionProfile {
	base := strings.ToLower(filepath.Base(strings.TrimPrefix(path, "file://")))
	ext := filepath.Ext(base)
	switch {
	case isCodePath(path), codeCorpus && projectOnlyExts[ext]:
		return codeProfile
	case captionImageExts[ext]:
		return imageProfile
	case codeCorpus && (proseExts[ext] || ext == "" || strings.HasPrefix(base, "license")):
		return docsProfile
	}
	return legalProfile
}

// codeCorpusShare is the share of an index's documents that must be source
// files for the index to count as a software project. A quarter, not a
// majority: a repository carries docs, images and data beside its code.
// ASSUMPTION (2026-10-20): no case folder holds that many source files.
const codeCorpusShare = 0.25

// isCodeCorpus reports whether this index is a software project, by the share
// of its documents that are source files.
func (s *Store) isCodeCorpus() bool {
	rows, err := s.db.Query(`SELECT path FROM documents`)
	if err != nil {
		return false
	}
	defer rows.Close()
	var n, code int
	for rows.Next() {
		var p string
		if rows.Scan(&p) != nil {
			return false
		}
		n++
		if isCodePath(p) {
			code++
		}
	}
	return n > 0 && float64(code) >= codeCorpusShare*float64(n)
}

// pathForPrompt is the tail of a path, enough to say where a file sits in its
// project without a home directory in the prompt.
func pathForPrompt(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "file://"), "/")
	if len(parts) > 4 {
		parts = parts[len(parts)-4:]
	}
	return strings.Join(parts, "/")
}

// allKindNames is every profile's vocabulary, for a person's kind (any family
// is theirs to choose) and a help string.
func allKindNames() []string {
	var out []string
	for _, p := range captionProfiles {
		out = append(out, p.kindNames()...)
	}
	return out
}

// NormalizeAnyKind maps a kind onto the first profile that knows it. A bare
// legal term ("deed", "court filing") is accepted for what was stored before
// kinds had families.
func NormalizeAnyKind(s string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	if fam, _, ok := strings.Cut(k, ":"); ok {
		for _, p := range captionProfiles {
			if p.Family == fam {
				return p.normalize(k)
			}
		}
		return "", false
	}
	return legalProfile.normalize(k)
}
