package raglit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THIS IS A LIBRARY. A real matter never enters it.
//
// raglit is retrieval over a document corpus. It has no business knowing a party's
// name, a surveyor's certificate number, or a law firm's phone number — and on
// 2026-09-06 it held all three, in a public repository, because it was developed
// against a live corpus and its tests and prose recorded what they found there.
// A survey sheet became a fixture; a misread name became an assertion; a phone
// number became an example of a claim that does not name its subject.
//
// caselit has carried this rule and a test for it since the corpus work started.
// The libraries had neither, which is the wrong way round: they are the ones that
// are public.
//
// WHAT A BLOCKLIST CANNOT DO, said here so nobody trusts it further than it goes.
// It catches the identifiers somebody already found. Three passes over these
// repositories in one day each turned up a category the previous pass had no
// entry for — surnames, then given names and images, then a phone number, a
// docket and a plat name. The SHAPE checks below are the half that generalises,
// and the habit is the half that actually works.

// blockedHashes are identifiers from real matters, stored as truncated SHA-256
// of the lower-cased term.
//
// HASHED BECAUSE THIS FILE IS PUBLISHED. A plaintext blocklist is a labelled
// list of real parties — "identifiers from real matters", followed by the
// surnames — and publishing it in a repository whose commits carry the author's
// name associates named individuals with litigation. That is a more direct
// disclosure than most of what this test exists to catch, and it would have gone
// out inside the guard against it.
//
// This is not strong against somebody who brings a surname dictionary and means
// it. It is not meant to be: it removes the READABLE, labelled disclosure, which
// is the harm that actually happens. Nobody stumbles over a hash.
//
// The plaintext list lives in caselit, which is private and is where the
// construction work happens.
var blockedHashes = map[string]bool{
	"0e6049bb86d24f62": true, "bba7545029f60a7d": true,
	"03aea87c85727901": true, "6f42e6cb034d40a4": true,
	"c1d1af2be530ba79": true, "ce98a9fd1f8742cb": true,
	"655f09716b4685ee": true, "3b02952b8d9390ff": true,
	"736307e4f8677bab": true, "e5585ed39e668d35": true,
	"cddb8ff901db81de": true, "ab47633591bec8bb": true,
	"017f5542ab70877b": true, "faf3f5498bb26302": true,
	"7de17c89d91da9d7": true, "ed50f7feb81347f3": true,
	"962314b7ef2f7000": true, "0d098b1c0162939e": true,
	"7904841598a49a87": true, "208ad55e4c4200ca": true,
	"01621148306fc8fb": true, "d4644953c0e692ee": true,
	"e736b4bdaae9dcfb": true, "d925e9127d186ced": true,
	"74a5e7366ce2d385": true, "1b24ee4f556f61db": true,
	"87cde5b60247dc2c": true, "9ad378e6dab4ce5c": true,
	"863eff8594345c5b": true, "3b732ab4bfdb8e7f": true,
	"d457f4d90af4f8e6": true, "af53f6291e55c223": true,
	"8c95cda58df92327": true, "4055a7ed17b15394": true,
	"6fb016be42243c0a": true, "91933b0c7c608a20": true,
	"7eae7a75789df006": true, "6bba89f798b0df0b": true,
	"73ee9151c1a38cc5": true, "3c44140b791e645f": true,
	"17debb5ef33409e5": true, "ad88357a9dad1b3c": true,
}

// hashTerm is how a candidate is compared. Truncated to 16 hex characters, which
// is 64 bits — collisions are not a concern for a list this size, and a full
// digest per entry makes the table unreadable for no gain.
func hashTerm(s string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(s)))
	return hex.EncodeToString(sum[:])[:16]
}

// wordRe pulls the candidates out of a file. Single words of four or more
// letters, plus adjacent pairs, because some entries are two words
// (a firm name, say) and a per-word check would miss them while a per-line check
// would drown in false positives.
var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z'-]{2,}`)

// blockedIn reports the first blocked term a body contains, hashed.
func blockedIn(body string) string {
	words := wordRe.FindAllString(body, -1)
	for i, w := range words {
		if blockedHashes[hashTerm(w)] {
			return w
		}
		if i+1 < len(words) {
			if pair := w + " " + words[i+1]; blockedHashes[hashTerm(pair)] {
				return pair
			}
		}
	}
	// The numeric and hyphenated entries — instrument numbers, dockets, a phone
	// — are not words, so they are matched on their own shape.
	for _, tok := range tokenRe.FindAllString(body, -1) {
		if blockedHashes[hashTerm(tok)] {
			return tok
		}
	}
	return ""
}

var tokenRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9-]{5,}`)

// shapes are the checks that generalise past a list. Each is deliberately
// high-precision: a false positive here costs more than a missed line, because it
// is what teaches somebody to add a `//nolint` and stop reading.
var shapes = []struct {
	name string
	re   *regexp.Regexp
	why  string
}{
	{"a phone number", regexp.MustCompile(`\b\(?[2-9][0-9]{2}\)?[ .-](?:0[0-9]{2}|[1-9][0-9]{2})-[0-9]{4}\b`),
		"use 555-01xx, which is reserved for fiction and cannot ring anybody"},
	{"a ZIP+4", regexp.MustCompile(`\b[0-9]{5}-[0-9]{4}\b`),
		"a ZIP+4 is a side of a street; invent one outside the assigned ranges"},
	// `court` is NOT a street suffix here. "466 Supreme Court opinions" is not an
	// address, and in a legal corpus that false positive fires constantly. The
	// cost is a missed address on a street called Court; the blocklist and the
	// habit cover that.
	{"a street address", regexp.MustCompile(`(?i)\b[0-9]{2,5} +(north|south|east|west|[NSEW]\.?) *[A-Z][a-z]+ +(st|street|ave|avenue|rd|road|ln|lane|dr|drive|way|blvd)\b`),
		"a property is identifiable from its address alone"},
	{"an auditor file number", regexp.MustCompile(`(?i)\bAF ?#? ?(19|20)[0-9]{8,10}\b`),
		"recorded instruments name their parties; invent the number"},
}

// invented are the fictional values this repository uses on purpose.
//
// DENY BY DEFAULT, ALLOW THE FICTION. The shape checks above cannot tell an
// invented instrument number from a recorded one — nothing can, that is what a
// number is — so every one has to be written down here, and a new one fails the
// build until somebody adds it. That failure IS the check: it is the moment a
// person has to answer "did I make this up, or did I read it off a document?",
// which is the question that was never asked the first time round.
//
// The 555-01xx block is reserved by the ITU for fiction and cannot ring anybody.
var invented = map[string]bool{
	"201404150061": true, "201503110042": true, "201503110043": true,
	"201503110044": true, "201503110045": true, "201503110046": true,
	"20150311004": true, "2015031104": true, "20150311": true,
	"201907220015": true, "201907220016": true, "201907220017": true,
	"201907220018": true, "9404150061": true, "40118802": true,
	"555-010-2288": true, "555-010-4477": true, "98999-0100": true,
	"5550104477": true, "5550102288": true,
	"1440 north northlea road": true, "1440 north northlea rd": true,
	"18 sedge street": true,
}

var nonDigit = regexp.MustCompile(`[^0-9]`)

// isInvented reports whether a shape match is one this repository made up.
//
// It compares two normalisations, because the same invented value is written
// several ways: `(555) 010-4477` and `555-010-4477` are one number, and an
// address is matched as text. Comparing the raw match would make the allowlist a
// list of spellings rather than a list of decisions.
func isInvented(match string) bool {
	m := strings.ToLower(strings.Join(strings.Fields(match), " "))
	if invented[m] {
		return true
	}
	return invented[nonDigit.ReplaceAllString(match, "")]
}

// ownShapes are the AUTHOR's exposure rather than a client's, and they are a
// separate list because the reason differs.
//
// A LIBRARY HAS NO INTERNAL HOSTNAME. Shipping one as a default publishes an
// infrastructure map and quietly points a stranger's run at somebody's private
// endpoint. raglit's `--llm-url` help named a private gateway; the flag's real
// default was empty all along, so removing the leak also fixed the docs.
//
// A specific private IP is a machine on somebody's LAN. `192.168.x` is generic
// by definition, so the check is on the octets that identify a host, and a
// documentation-range address passes.
//
// An absolute home path names a user and does not work for anybody else, which
// is two problems in one string.
//
// THE AUTHOR'S NAME IS NOT HERE, deliberately. It is authorship: every commit
// carries it, and scrubbing the tree while `git log` says it on every line is
// theatre. Publishing pseudonymously is a different and larger decision.
var ownShapes = []struct {
	name string
	re   *regexp.Regexp
	why  string
}{
	{"an internal hostname", regexp.MustCompile(`\b[a-z0-9-]+\.(iodesystems\.com|lan|internal|corp)\b`),
		"take it from the environment; a library must not ship somebody's endpoint"},
	{"an absolute home path", regexp.MustCompile(`/(home|Users)/[a-z][a-z0-9_-]*/[A-Za-z0-9_.-]`),
		"use $HOME or a relative path; an absolute one names a user and works for nobody else"},
}

// okHomePath allows the placeholder forms, so the check lands on real usernames.
var okHomePath = regexp.MustCompile(`/(home|Users)/(someone|user|you|me|example|u)/`)

// okEmail is where a test address may point. Anything else is somebody's inbox.
var okEmail = regexp.MustCompile(`@(example\.(com|org|net|test)|invalid|localhost|iodesystems\.com|github\.com|[0-9]+\.service)\b`)

var emailRe = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

// documentExts are file kinds that carry a document's own image. THIS IS THE
// CHECK THE PROSE PASS CANNOT BE.
//
// Scrubbing text does nothing to a scan. Two 3400x4400 renders of a recorded
// survey sat in raglit's bench fixtures through a whole scrub, and the probe
// text beside them had already written down that the pseudonym "had been applied
// to the prose in this repository but not to the fixture the prose describes".
// The note outlived the mismatch it recorded.
//
// A library has no reason to commit one. Rendered fixtures are generated from an
// operator's own corpus at run time.
var documentExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".webp": true, ".heic": true, ".tif": true, ".tiff": true,
	".pdf": true, ".mp4": true, ".mov": true, ".m4a": true, ".wav": true,
}

func TestNoRealMatterDetail(t *testing.T) {
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	self := "noleak_test.go"

	for _, f := range strings.Split(string(out), "\n") {
		f = strings.TrimSpace(f)
		if f == "" || f == self {
			continue
		}

		// The image check runs on the PATH and needs no read, so it covers the
		// files every other check here is blind to.
		if documentExts[strings.ToLower(filepath.Ext(f))] {
			t.Errorf("%s is a document or image, committed.\n"+
				"    Scrubbing prose does not reach a scan. A library does not carry one:\n"+
				"    generate it from an operator's own corpus, and gitignore the output.", f)
			continue
		}

		b, rerr := os.ReadFile(f) //nolint:gosec // a path git itself listed
		if rerr != nil {
			continue
		}
		body := string(b)

		if bad := blockedIn(body); bad != "" {
			t.Errorf("%s names %q, which belongs to a real matter.\n"+
				"    What the corpus TAUGHT is welcome here; what it IS is not.", f, bad)
		}
		for _, s := range ownShapes {
			for _, m := range s.re.FindAllString(body, -1) {
				if okHomePath.MatchString(m) || isInvented(m) {
					continue
				}
				t.Errorf("%s carries %s (%q). %s", f, s.name, m, s.why)
			}
		}
		for _, s := range shapes {
			for _, m := range s.re.FindAllString(body, -1) {
				if isInvented(m) {
					continue
				}
				t.Errorf("%s carries %s (%q). %s\n"+
					"    If you invented it, add it to `invented` — that is the check.", f, s.name, m, s.why)
			}
		}
		for _, addr := range emailRe.FindAllString(body, -1) {
			if !okEmail.MatchString(addr) {
				t.Errorf("%s carries the address %q, which is somebody's inbox.\n"+
					"    Use an example.com address.", f, addr)
			}
		}
	}
}

// TestTheGuardsOwnPatternsStillBite — who watches the watchman.
//
// A DEAD PATTERN PASSES SILENTLY, which is the worst failure a guard can have:
// the build stays green and the check has stopped existing. It happened here
// while this file was being written. A `\b` written into a non-raw Python string
// became a literal backspace, so the internal-hostname regex read
// "<BS>[a-z0-9-]+\.(...)<BS>" and matched nothing. The test went green over a
// planted hostname and I only noticed because I was expecting a failure.
//
// So every shape carries a sample it MUST match and a sample it must NOT. The
// second half is not ceremony: a pattern that matches everything is as useless as
// one that matches nothing, and it is the version somebody disables.
func TestTheGuardsOwnPatternsStillBite(t *testing.T) {
	cases := []struct {
		name       string
		re         *regexp.Regexp
		hit, clean string
	}{
		{"a phone number", shapes[0].re, "call 206-555-0142 today", "version 2.0.1-rc4 shipped"},
		{"a ZIP+4", shapes[1].re, "Havern WA 98273-5461", "range 10000-20000 rows"},
		{"a street address", shapes[2].re, "at 1200 North Elmgrove Road", "466 Supreme Court opinions"},
		{"an auditor file number", shapes[3].re, "recorded AF 201501010001", "AF 123"},
		{"an internal hostname", ownShapes[0].re, "https://svc.internal/v1", "github.com/iodesystems/kgraph"},
		{"an absolute home path", ownShapes[1].re, "/home/someone/local/src", "./relative/path"},
	}
	for _, c := range cases {
		if !c.re.MatchString(c.hit) {
			t.Errorf("the %s check no longer matches %q — the pattern is dead and the\n"+
				"    build is green because of it. Check for a mangled escape.", c.name, c.hit)
		}
		if c.re.MatchString(c.clean) {
			t.Errorf("the %s check matches %q, which is clean. A pattern that cries wolf\n"+
				"    is one somebody switches off.", c.name, c.clean)
		}
	}
	// And the sets themselves, so a shape deleted in a refactor is caught rather
	// than quietly reducing what is checked.
	if len(shapes) != 4 || len(ownShapes) != 2 {
		t.Errorf("shapes=%d ownShapes=%d; a check was added or removed and this test "+
			"was not told", len(shapes), len(ownShapes))
	}
}
