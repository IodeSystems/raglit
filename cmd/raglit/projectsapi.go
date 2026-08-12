package main

// Projects, as the daemon can know them.
//
// A project is NOT stored anywhere. It is the "<project>__" prefix a client puts
// on every index name it sends, so that one per-user daemon can serve every
// project without their documents piling into a shared `default` — see
// namespace.go, which owns the convention. The daemon has always been
// deliberately project-agnostic: it opens indexes by name and asks no questions.
//
// That is fine for isolation and useless for a person looking at nine flat
// names. `delano-v-mckinnon__default`, `dun__dun`, `dun__dun-main`,
// `raglit__code` are four projects' worth of corpus rendered as one list, and
// nothing says which indexes belong together or which of them a project is
// actually watching.
//
// So this DERIVES the hierarchy rather than adding storage for it: split each
// index name on the separator, group, and enrich from the watch registry, which
// is the one place the daemon does learn a project's home on disk. A project
// nobody has registered still appears — it has indexes, which is what makes it a
// project — it simply has no home to show.
//
// The alternative was a projects table, and it was rejected for a reason worth
// keeping: the prefix is already the source of truth, it is what routing and
// isolation actually key on, and a second record of the same fact is a second
// thing to get out of step. Nothing here can disagree with the index names.

import (
	"context"
	"sort"
	"strings"

	"github.com/iodesystems/raglit"
)

// projectIndexRow is one index inside a project, under its LOCAL name.
//
// Both names are carried. Local ("default") is what a person in that project
// calls it and what the CLI takes; Name ("delano-v-mckinnon__default") is what
// every daemon endpoint takes. Showing only the local name would make the UI's
// own links unbuildable; showing only the daemon name is the wall this exists to
// replace.
type projectIndexRow struct {
	Name      string `json:"name"`
	Local     string `json:"local"`
	Documents int    `json:"documents"`
	Fragments int    `json:"fragments"`
	Pending   int    `json:"pending"`
	Running   int    `json:"running"`
	Failed    int    `json:"failed"`
	// Parent is set when this index is a BRANCH of another, and empty otherwise.
	// A branch is a real index in its own right — it appears in reg.Names() and
	// answers every endpoint — so without this the UI would list a branch beside
	// the index it overlays as though they were siblings.
	Parent string `json:"parent,omitempty"`
}

type projectRow struct {
	// Name is the namespace prefix. Empty means the indexes that carry no prefix
	// at all — `default` on this daemon. They are a real group and they are NOT a
	// project; the UI names them for what they are rather than inventing one.
	Name      string            `json:"name"`
	Indexes   []projectIndexRow `json:"indexes"`
	Documents int               `json:"documents"`
	Fragments int               `json:"fragments"`
	Pending   int               `json:"pending"`
	Running   int               `json:"running"`
	Failed    int               `json:"failed"`
	// Home and Watching come from the watch registry, and are absent for a
	// project that has never registered one. Absent is not "not watching" — it is
	// "this daemon has never been told where that project lives" — so the two are
	// separate fields rather than one tri-state.
	Home     string `json:"home,omitempty"`
	Watching bool   `json:"watching"`
	Files    int    `json:"files,omitempty"`
}

type listProjectsOut struct {
	Body struct {
		Projects []projectRow `json:"projects"`
	}
}

// splitNS splits a daemon index name into (namespace, local). A name with no
// separator has no namespace — it is not "a project called default", it is an
// index nobody namespaced.
func splitNS(name string) (ns, local string) {
	if i := strings.Index(name, nsSep); i >= 0 {
		return name[:i], name[i+len(nsSep):]
	}
	return "", name
}

func listProjectsOp(reg *raglit.Registry, w *watcher) func(context.Context, *struct{}) (*listProjectsOut, error) {
	return func(_ context.Context, _ *struct{}) (*listProjectsOut, error) {
		// Branch lineage first: a branch is indistinguishable from a plain index
		// by name, so the only way to know is to ask the registry.
		parentOf := map[string]string{}
		if bs, err := reg.ListBranches(); err == nil {
			for _, b := range bs {
				parentOf[b.Name] = b.Parent
			}
		}

		byNS := map[string]*projectRow{}
		for _, name := range reg.Names() {
			ns, local := splitNS(name)
			p := byNS[ns]
			if p == nil {
				p = &projectRow{Name: ns, Indexes: []projectIndexRow{}}
				byNS[ns] = p
			}
			row := projectIndexRow{Name: name, Local: local, Parent: parentOf[name]}
			if st, err := reg.Get(name); err == nil {
				if s, err := st.IndexStatus(); err == nil {
					row.Documents, row.Fragments = s.Documents, s.Fragments
					row.Pending, row.Running, row.Failed = s.Pending, s.Running, s.Failed
				}
			}
			// A branch stores only its diffs, so its counts are the branch's OWN
			// rows and adding them to the project would double-count what it
			// overlays. Rolled up from root indexes only.
			if row.Parent == "" {
				p.Documents += row.Documents
				p.Fragments += row.Fragments
			}
			p.Pending += row.Pending
			p.Running += row.Running
			p.Failed += row.Failed
			p.Indexes = append(p.Indexes, row)
		}

		if w != nil {
			for _, wi := range w.List() {
				if p := byNS[wi.Project]; p != nil {
					p.Home, p.Watching, p.Files = wi.Home, wi.Watching, wi.Files
				}
			}
		}

		out := &listProjectsOut{}
		out.Body.Projects = make([]projectRow, 0, len(byNS))
		for _, p := range byNS {
			sort.Slice(p.Indexes, func(i, j int) bool { return p.Indexes[i].Local < p.Indexes[j].Local })
			out.Body.Projects = append(out.Body.Projects, *p)
		}
		// Unnamespaced last: it is the leftover, not the headline.
		sort.Slice(out.Body.Projects, func(i, j int) bool {
			a, b := out.Body.Projects[i].Name, out.Body.Projects[j].Name
			if (a == "") != (b == "") {
				return b == ""
			}
			return a < b
		})
		return out, nil
	}
}
