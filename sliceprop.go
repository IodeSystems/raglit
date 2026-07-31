package raglit

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Proposing slices from what the pages say about themselves.
//
// A packet of standard forms carries its own structure: every NWMLS form prints
// "Page 1 of 6" through "Page 6 of 6", and the counter RESETTING to 1 is the
// boundary. Nothing has to be inferred from the prose, and no model is involved —
// the printer already wrote down where each instrument starts and how long it is.
//
// Measured on the 2021 PSA offer: Form 21 (1 of 6 → 6 of 6), Form 22A (1 of 3 →
// 3 of 3), Form 22D, 22P, 22T, 34, 22J, 35 — seven-plus instruments in one
// 30-page packet, every boundary readable from the indexed text.
//
// It PROPOSES and never records. A boundary is a claim about what a document is,
// which is a ruling; and the counter lies often enough — a form reprinted inside
// another, a page carrying the tail of one form and the head of the next — that
// a queue somebody skims is the right output and an automatic declaration is not.

var (
	formRe   = regexp.MustCompile(`(?i)\bform\s+(\d+[A-Z]*)\b`)
	pageOfRe = regexp.MustCompile(`(?i)\bpage\s+(\d+)\s+of\s+(\d+)\b`)
)

// formStart is a form declaring its own first page on a sheet.
type formStart struct {
	form  string
	total int
}

// SliceProposal is a candidate instrument inside a bundle.
type SliceProposal struct {
	From  int    `json:"from"`
	To    int    `json:"to"`
	Form  string `json:"form,omitempty"`
	Title string `json:"title"`
	Why   string `json:"why"`
	// Shared names the other forms that start on this proposal's FIRST page.
	//
	// Two one-page forms printed on one sheet cannot both be a page range, and
	// the answer is not to pick one: a slice mints a document from a range, and
	// the range would be a lie about half of it. Those are `seen-in` claims —
	// observed inside, no identity minted, no page arithmetic — so the proposal
	// says so rather than quietly proposing a slice that is wrong.
	Shared []string `json:"shared_page_with,omitempty"`
}

// Sliceable reports whether this proposal should become a slice at all.
func (p SliceProposal) Sliceable() bool { return len(p.Shared) == 0 }

// ProposeSlices reads a bundle's pages and proposes the instruments in it.
func ProposeSlices(pages []PageText) []SliceProposal {
	// starts[page] = the forms that declare "page 1 of N" on that page.
	starts := map[int][]formStart{}
	maxPage := 0
	for _, pg := range pages {
		if pg.Page > maxPage {
			maxPage = pg.Page
		}
		flat := strings.Join(strings.Fields(pg.Text), " ")
		formAt := formRe.FindAllStringSubmatchIndex(flat, -1)
		for _, m := range pageOfRe.FindAllStringSubmatchIndex(flat, -1) {
			n, _ := strconv.Atoi(flat[m[2]:m[3]])
			total, _ := strconv.Atoi(flat[m[4]:m[5]])
			if n != 1 || total < 1 {
				continue
			}
			// Attribute the counter to the NEAREST form marker, not to the last
			// one on the page. Two one-page forms printed on one sheet both say
			// "Page 1 of 1", and taking the last would collapse them into one —
			// which is exactly the case that must NOT become a single slice.
			//
			// With no form marker at all the run is still a run — a packet's cover
			// pages carry counters and no form number — so it is proposed unnamed
			// rather than dropped.
			// Nearest PRECEDING form marker: a form prints its own header above
			// its counter, so the following marker belongs to the next form. Bare
			// proximity picks that one whenever two forms share a sheet, which is
			// precisely the case this has to get right.
			name := ""
			best := -1
			for _, f := range formAt {
				if f[0] > m[0] {
					continue
				}
				if d := m[0] - f[0]; best < 0 || d < best {
					best, name = d, strings.ToUpper(flat[f[2]:f[3]])
				}
			}
			if name == "" {
				// No form above it — take the first below, so a counter under a
				// header split across pages still gets a name.
				for _, f := range formAt {
					if f[0] > m[0] {
						name = strings.ToUpper(flat[f[2]:f[3]])
						break
					}
				}
			}
			if !hasStart(starts[pg.Page], name, total) {
				starts[pg.Page] = append(starts[pg.Page], formStart{form: name, total: total})
			}
		}
	}

	var firsts []int
	for p := range starts {
		firsts = append(firsts, p)
	}
	sort.Ints(firsts)

	var out []SliceProposal
	for i, p := range firsts {
		for _, st := range starts[p] {
			to := p + st.total - 1
			// The declared length is a claim by the form, and the next form's
			// start is a fact about the packet. Where they disagree, trust the
			// packet: a form reprinted inside another would otherwise swallow it.
			if i+1 < len(firsts) && firsts[i+1] <= to {
				to = firsts[i+1] - 1
			}
			if to > maxPage {
				to = maxPage
			}
			if to < p {
				to = p
			}
			pr := SliceProposal{
				From: p, To: to, Form: st.form,
				Why: fmt.Sprintf("declares page 1 of %d on page %d", st.total, p),
			}
			if st.form != "" {
				pr.Title = "Form " + st.form
			} else {
				pr.Title = fmt.Sprintf("pages %d-%d", p, to)
			}
			out = append(out, pr)
		}
	}

	// Mark the sheets where more than one instrument begins.
	byFirst := map[int][]string{}
	for _, p := range out {
		if p.Form != "" {
			byFirst[p.From] = append(byFirst[p.From], p.Form)
		}
	}
	for i := range out {
		others := []string{}
		for _, f := range byFirst[out[i].From] {
			if f != out[i].Form {
				others = append(others, f)
			}
		}
		if len(others) > 0 {
			sort.Strings(others)
			out[i].Shared = others
			out[i].Why += fmt.Sprintf("; shares that sheet with Form %s", strings.Join(others, ", "))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].Form < out[j].Form
	})
	return out
}

func hasStart(ss []formStart, form string, total int) bool {
	for _, s := range ss {
		if s.form == form && s.total == total {
			return true
		}
	}
	return false
}
